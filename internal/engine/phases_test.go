package engine

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hyukvoid/idemcheck/internal/config"
	"github.com/hyukvoid/idemcheck/internal/models"
)

// keyedServer counts requests per idempotency key and always answers with
// one constant logical result (the endpoint behaves correctly).
func keyedServer(perKey *map[string]int, mu *sync.Mutex) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Idempotency-Key")
		mu.Lock()
		(*perKey)[key]++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true}`)
	}))
}

// In a burst any response can end up in the baseline slot. Whether the
// REPLAY runs must therefore depend on evidence content (policy status
// classes), never on which response was observed first — otherwise the
// same evidence would produce different verdicts on different runs.
func TestReplayTriggerIgnoresObservationOrder(t *testing.T) {
	var mu sync.Mutex
	perKey := map[string]int{}
	srv := keyedServer(&perKey, &mu)
	defer srv.Close()

	// finalize runs the check's settle/replay/evaluate pipeline for a
	// crafted evidence set with an explicit baseline status.
	finalize := func(t *testing.T, res *CheckResult, pol config.Policy) {
		t.Helper()
		r := newRunner(srv.URL, `{"item_id":42}`)
		r.ReplayTimeout = time.Second
		key := configKey(r.BaseKey, ScConcurrent)
		if err := r.finalizeSameKey(context.Background(), res, r.Spec, key, pol, true); err != nil {
			t.Fatal(err)
		}
	}
	phases := func(res *CheckResult) string { return strings.Join(res.ActionPhases, "\n") }

	transient := group(429, `{"error":"rate limited"}`, 1)
	success := group(200, `{"ok":true}`, 1)

	t.Run("transient observed first still replays and confirms", func(t *testing.T) {
		res := result(success, transient)
		res.BaselineStatus = 429 // the rate-limited answer was observed first
		finalize(t, res, safePolicy(t))
		if !strings.Contains(phases(res), "REPLAY:") {
			t.Fatalf("policy transient baseline must not block the replay:\n%s", phases(res))
		}
		if res.Status != models.StatusPass {
			t.Fatalf("status = %s (%s), want PASS confirmed by replay", res.Status, res.Detail)
		}
		if res.Replay == nil || !res.Replay.Confirmed {
			t.Errorf("replay info = %+v, want confirmed", res.Replay)
		}
	})

	t.Run("same evidence with the success observed first also passes", func(t *testing.T) {
		res := result(success, transient)
		res.BaselineStatus = 200 // arrival order flipped, evidence identical
		finalize(t, res, safePolicy(t))
		if res.Status != models.StatusPass {
			t.Fatalf("status = %s (%s): identical evidence must give identical verdicts", res.Status, res.Detail)
		}
	})

	t.Run("non-policy rejection still blocks the replay", func(t *testing.T) {
		res := result(success, group(500, `{"error":"boom"}`, 1))
		res.BaselineStatus = 500
		finalize(t, res, safePolicy(t))
		if strings.Contains(phases(res), "REPLAY:") {
			t.Errorf("a 500 baseline must still block the replay:\n%s", phases(res))
		}
		if res.Status != models.StatusInconclusive {
			t.Fatalf("status = %s (%s), want INCONCLUSIVE (500 is unknown to the policy)", res.Status, res.Detail)
		}
	})

	t.Run("strict-replay blocks any >=400 baseline", func(t *testing.T) {
		res := result(success, transient)
		res.BaselineStatus = 429
		finalize(t, res, strictPolicy(t))
		if strings.Contains(phases(res), "REPLAY:") {
			t.Errorf("strict-replay tolerates no transients, so 429 must block the replay:\n%s", phases(res))
		}
		if res.Status != models.StatusInconclusive {
			t.Fatalf("status = %s (%s), want INCONCLUSIVE under strict-replay", res.Status, res.Detail)
		}
	})
}

// A single trial must expose the full phase chain: BURST (released
// together), SETTLE, REPLAY (confirming the stored result) and a verdict
// that notes the replay agreed.
func TestConcurrentSingleTrialPhases(t *testing.T) {
	perKey := map[string]int{}
	var mu sync.Mutex
	srv := keyedServer(&perKey, &mu)
	defer srv.Close()

	r := newRunner(srv.URL, `{"item_id":42}`)
	r.Settle = 10 * time.Millisecond
	r.ReplayTimeout = time.Second

	res, err := r.runConcurrentTrials(context.Background(), config.DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != models.StatusPass {
		t.Fatalf("status = %s (%s), want PASS", res.Status, res.Detail)
	}
	if res.Trials != 1 {
		t.Errorf("trials = %d, want 1", res.Trials)
	}
	// Concurrency burst (4) + one replay.
	if res.Requests != r.Concurrency+1 {
		t.Errorf("requests = %d, want %d", res.Requests, r.Concurrency+1)
	}

	phases := strings.Join(res.ActionPhases, "\n")
	for _, want := range []string{"BURST:", "SETTLE:", "REPLAY:"} {
		if !strings.Contains(phases, want) {
			t.Errorf("missing phase %q in:\n%s", want, phases)
		}
	}
	if !strings.Contains(res.ActionPhases[0], fmt.Sprintf("%d requests released together", r.Concurrency)) {
		t.Errorf("BURST phase must state the burst size: %s", res.ActionPhases[0])
	}
	if !strings.Contains(res.Detail, "replay agreed") {
		t.Errorf("verdict must note the replay agreed: %s", res.Detail)
	}
	// VERDICT is appended by the report model.
	if !strings.Contains(strings.Join(res.ToModel().Phases, "\n"), "VERDICT:") {
		t.Errorf("model phases missing VERDICT: %v", res.ToModel().Phases)
	}
}

// Multiple trials must use an isolated idempotency key per burst so
// server-side state from one trial can never mask the next, and the
// aggregate must report the trial count.
func TestConcurrentTrialsIsolatedKeys(t *testing.T) {
	perKey := map[string]int{}
	var mu sync.Mutex
	srv := keyedServer(&perKey, &mu)
	defer srv.Close()

	r := newRunner(srv.URL, `{"item_id":42}`)
	r.Trials = 3
	r.Settle = 10 * time.Millisecond
	r.ReplayTimeout = time.Second

	res, err := r.runConcurrentTrials(context.Background(), config.DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != models.StatusPass {
		t.Fatalf("status = %s (%s), want PASS", res.Status, res.Detail)
	}
	if res.Trials != 3 {
		t.Errorf("trials = %d, want 3", res.Trials)
	}
	if want := 3 * (r.Concurrency + 1); res.Requests != want {
		t.Errorf("requests = %d, want %d (3 trials × (burst+replay))", res.Requests, want)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(perKey) != 3 {
		t.Fatalf("distinct keys = %d, want 3 (one per trial): %v", len(perKey), perKey)
	}
	for k, n := range perKey {
		if want := r.Concurrency + 1; n != want {
			t.Errorf("key %q seen %d times, want %d (burst+replay share the trial key)", k, n, want)
		}
	}

	if !strings.Contains(res.Detail, "No observable race in 3 trials") {
		t.Errorf("aggregate detail = %q", res.Detail)
	}
	phases := strings.Join(res.ActionPhases, "\n")
	if !strings.Contains(phases, "3 requests") && !strings.Contains(phases, "× 3 trials") {
		t.Errorf("aggregate phases must state the trial count:\n%s", phases)
	}
}

// A diverging endpoint must FAIL the concurrent check even though every
// individual response is a 2xx: bodies decide, status class does not.
func TestConcurrentTrialDetectsDivergence(t *testing.T) {
	var mu sync.Mutex
	next := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		next++
		id := next
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"order_id":%d}`, id)
	}))
	defer srv.Close()

	r := newRunner(srv.URL, `{"item_id":42}`)
	r.ReplayTimeout = time.Second
	res, err := r.runConcurrentTrials(context.Background(), config.DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != models.StatusFail {
		t.Fatalf("status = %s (%s), want FAIL", res.Status, res.Detail)
	}
	if res.Logical < 2 {
		t.Errorf("logical = %d, want >= 2", res.Logical)
	}
}
