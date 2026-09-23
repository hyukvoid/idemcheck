package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hyukvoid/idemcheck/internal/config"
	"github.com/hyukvoid/idemcheck/internal/fingerprint"
	"github.com/hyukvoid/idemcheck/internal/httpx"
	"github.com/hyukvoid/idemcheck/internal/models"
)

func newRunner(url, body string) *Runner {
	return &Runner{
		Client: httpx.NewClient(5 * time.Second),
		Spec: httpx.Spec{
			Method:    "POST",
			URL:       url,
			Body:      []byte(body),
			KeyHeader: "Idempotency-Key",
			Header:    http.Header{},
		},
		FPOpts:      fingerprint.Options{},
		BaseKey:     config.NewKey(),
		Repeat:      4,
		Concurrency: 4,
		Policy:      config.DefaultPolicy(),
	}
}

// handleVariant inspects the incoming body for the engine's payload marker.
func handleVariant(r *http.Request) (body []byte, variant bool) {
	raw := make([]byte, 0, 512)
	{
		b := make([]byte, 512)
		n, _ := r.Body.Read(b)
		raw = b[:n]
	}
	var tree map[string]any
	if err := json.Unmarshal(raw, &tree); err == nil {
		_, variant = tree["idemcheck_variant"]
	}
	return raw, variant
}

// rejected: the endpoint refuses a modified payload under a reused key
// (409 Conflict) -> the classic safe contract, PASS.
func TestPayloadConflictRejectedPass(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, variant := handleVariant(r)
		key := r.Header.Get("Idempotency-Key")
		mu.Lock()
		first := !seen[key]
		seen[key] = true
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if variant {
			w.WriteHeader(http.StatusConflict)
			fmt.Fprint(w, `{"error":"idempotency key reused with a different payload"}`)
			return
		}
		if !first {
			// Replay the stored response for the same payload+key.
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"order_id":401}`)
			return
		}
		_ = raw
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"order_id":401}`)
	}))
	defer srv.Close()

	r := newRunner(srv.URL, `{"item_id":42}`)
	res, err := r.runPayloadConflict(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != models.StatusPass {
		t.Fatalf("rejected payload must PASS, got %s (%s)", res.Status, res.Detail)
	}
	if !strings.Contains(res.Detail, "rejected") {
		t.Errorf("detail should say rejected: %s", res.Detail)
	}
}

// accepted-same: the modified payload replays the identical logical result.
func TestPayloadAcceptedSamePass(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// Same logical answer no matter the payload.
		fmt.Fprint(w, `{"order_id":401,"status":"created"}`)
	}))
	defer srv.Close()

	r := newRunner(srv.URL, `{"item_id":42}`)
	res, err := r.runPayloadConflict(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != models.StatusPass {
		t.Fatalf("same logical result must PASS, got %s (%s)", res.Status, res.Detail)
	}
}

// accepted-different: the endpoint creates a second resource for the same
// key with a changed payload -> concrete divergence, FAIL (was WARN).
func TestPayloadAcceptedDifferentFails(t *testing.T) {
	var mu sync.Mutex
	next := 400
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, variant := handleVariant(r)
		mu.Lock()
		next++
		id := next
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		if variant {
			fmt.Fprintf(w, `{"order_id":%d,"note":"second resource"}`, id)
			return
		}
		fmt.Fprintf(w, `{"order_id":%d}`, id)
	}))
	defer srv.Close()

	r := newRunner(srv.URL, `{"item_id":42}`)
	res, err := r.runPayloadConflict(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != models.StatusFail {
		t.Fatalf("different logical results must FAIL, got %s (%s)", res.Status, res.Detail)
	}
	if res.Logical != 2 {
		t.Errorf("logical = %d, want 2", res.Logical)
	}
	if len(res.DifferingFields) == 0 {
		t.Error("FAIL must carry differing-field evidence")
	}
}

// inconclusive bucket: transient (429) or unknown (5xx) answers on the
// modified payload cannot establish a contract either way.
func TestPayloadTransientInconclusive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, variant := handleVariant(r)
		w.Header().Set("Content-Type", "application/json")
		if variant {
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error":"rate limited"}`)
			return
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"order_id":401}`)
	}))
	defer srv.Close()

	r := newRunner(srv.URL, `{"item_id":42}`)
	res, err := r.runPayloadConflict(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != models.StatusInconclusive {
		t.Fatalf("429 on modified payload must be INCONCLUSIVE, got %s (%s)", res.Status, res.Detail)
	}
}

// Transport failure during the payload check: insufficient evidence, not an
// execution error and never a pass.
func TestPayloadTransportInconclusive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "no hijack", 500)
			return
		}
		conn, _, _ := hj.Hijack()
		_ = conn.Close() // drop the connection without a response
	}))
	defer srv.Close()

	r := newRunner(srv.URL, `{"item_id":42}`)
	res, err := r.runPayloadConflict(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != models.StatusInconclusive {
		t.Fatalf("dropped connection must be INCONCLUSIVE, got %s (%s)", res.Status, res.Detail)
	}
}

// Control: different keys must yield distinguishable responses -> PASS.
func TestDistinctKeysControlPass(t *testing.T) {
	var mu sync.Mutex
	next := 400
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		next++
		id := next
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{"order_id":%d}`, id)
	}))
	defer srv.Close()

	r := newRunner(srv.URL, `{"item_id":42}`)
	res, err := r.runDistinctKeys(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != models.StatusPass {
		t.Fatalf("distinguishable keys must PASS, got %s (%s)", res.Status, res.Detail)
	}
}

// Control: constant responses make races invisible — the run must not
// report a pass on that basis (was WARN -> PASS aggregation).
func TestDistinctKeysControlIdenticalInconclusive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer srv.Close()

	r := newRunner(srv.URL, `{"item_id":42}`)
	res, err := r.runDistinctKeys(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != models.StatusInconclusive {
		t.Fatalf("identical control responses must be INCONCLUSIVE, got %s (%s)", res.Status, res.Detail)
	}
}
