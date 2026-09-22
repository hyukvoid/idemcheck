package engine

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hyukvoid/idemcheck/internal/fingerprint"
	"github.com/hyukvoid/idemcheck/internal/httpx"
)

func testSpec(url string) httpx.Spec {
	return httpx.Spec{
		Method:    "POST",
		URL:       url,
		Body:      []byte(`{"item_id":42}`),
		KeyHeader: "Idempotency-Key",
		Header:    http.Header{},
	}
}

// The barrier must hold every worker until all are ready, then release them
// together. A handler counts max simultaneous in-flight requests; without
// the barrier this number would be 1 (workers start staggered).
func TestConcurrentBarrierReleasesTogether(t *testing.T) {
	var inFlight, maxInFlight atomic.Int64
	release := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := inFlight.Add(1)
		for {
			old := maxInFlight.Load()
			if n <= old || maxInFlight.CompareAndSwap(old, n) {
				break
			}
		}
		<-release // hold every request until the test opens the gate
		inFlight.Add(-1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"ok":true}`)
	}))
	defer srv.Close()

	client := httpx.NewClient(5 * time.Second)
	const n = 8
	errCh := make(chan error, 1)
	go func() {
		time.Sleep(300 * time.Millisecond) // give all workers time to park
		close(release)
		errCh <- nil
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	res, err := Concurrent(ctx, client, testSpec(srv.URL), "key-barrier", n, fingerprint.Options{})
	if err != nil {
		t.Fatalf("concurrent run: %v", err)
	}
	<-errCh

	if res.Requests != n {
		t.Fatalf("expected exactly %d requests, got %d", n, res.Requests)
	}
	if res.Observed != n {
		t.Fatalf("expected %d observed responses, got %d", n, res.Observed)
	}
	if res.Unique != 1 {
		t.Fatalf("expected 1 unique response, got %d", res.Unique)
	}
	if got := maxInFlight.Load(); got != n {
		t.Fatalf("barrier failed: max in-flight %d, want %d (workers did not overlap)", got, n)
	}
	if len(res.Timings) != n {
		t.Fatalf("expected %d timing records, got %d", n, len(res.Timings))
	}
}

// Exactly N requests must execute even when handlers are fast.
func TestConcurrentExecutesExactlyN(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer srv.Close()

	client := httpx.NewClient(5 * time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	const n = 25
	res, err := Concurrent(ctx, client, testSpec(srv.URL), "key-n", n, fingerprint.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := hits.Load(); got != n {
		t.Fatalf("server received %d requests, want %d", got, n)
	}
	if res.Requests != n || res.Unique != 1 {
		t.Fatalf("requests=%d unique=%d", res.Requests, res.Unique)
	}
}

// Context cancellation must abort the barrier without leaking goroutines
// or hanging the run.
func TestConcurrentContextCancellation(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
	}))
	defer srv.Close()
	defer close(block)

	client := httpx.NewClient(5 * time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	var err error
	go func() {
		defer close(done)
		_, err = Concurrent(ctx, client, testSpec(srv.URL), "key-cancel", 5, fingerprint.Options{})
	}()
	select {
	case <-done:
		// finished — either ctx error or partial results; must not hang
	case <-time.After(5 * time.Second):
		t.Fatal("Concurrent did not return after context cancellation")
	}
	_ = err
}

// Sequential: responses must be collected in order with correct counts.
func TestSequentialCounts(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer srv.Close()

	client := httpx.NewClient(5 * time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	res, err := Sequential(ctx, client, testSpec(srv.URL), "key-seq", 7, fingerprint.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 7 || res.Requests != 7 || res.Unique != 1 {
		t.Fatalf("hits=%d requests=%d unique=%d", hits.Load(), res.Requests, res.Unique)
	}
}

// Distinct responses must group with counts and expose differing fields.
func TestCollectGroupsAndEvidence(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Distinct order_id per hit; request_id is volatile and ignored.
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"order_id":%d,"request_id":"volatile"}`, time.Now().UnixNano()%1000)
	}))
	defer srv.Close()

	client := httpx.NewClient(5 * time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	opts := fingerprint.Options{IgnoreJSONPaths: []string{"$.request_id"}}
	res, err := Sequential(ctx, client, testSpec(srv.URL), "key-diff", 5, opts)
	if err != nil {
		t.Fatal(err)
	}
	if res.Unique < 2 {
		t.Fatalf("expected multiple unique responses, got %d", res.Unique)
	}
	total := 0
	for _, g := range res.Groups {
		total += g.Count
	}
	if total != 5 {
		t.Fatalf("group counts sum to %d, want 5", total)
	}
	if len(res.DifferingFields) == 0 || res.DifferingFields[0] != "$.order_id" {
		t.Fatalf("expected $.order_id in differing fields, got %v", res.DifferingFields)
	}
}
