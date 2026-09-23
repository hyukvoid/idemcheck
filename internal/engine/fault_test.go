package engine

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hyukvoid/idemcheck/internal/config"
	"github.com/hyukvoid/idemcheck/internal/httpx"
	"github.com/hyukvoid/idemcheck/internal/models"
)

// idempotentServer stores the first response per key and replays it: a
// correctly implemented endpoint the fault can be injected in front of.
func idempotentServer(hits *atomic.Int32) *httptest.Server {
	var mu sync.Mutex
	stored := map[string]string{}
	var next int32
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Idempotency-Key")
		mu.Lock()
		body, ok := stored[key]
		if !ok {
			next++
			body = fmt.Sprintf(`{"order_id":%d}`, next)
			stored[key] = body
		}
		mu.Unlock()
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, body)
	}))
}

// A response lost after the server did the work is NOT evidence of a race
// (the other responses agree) and NOT evidence of convergence either (that
// request was never observed). The verdict must be INCONCLUSIVE, with the
// transport failure named — never a pass, never a false failure.
func TestLostResponseIsInconclusiveNotPassNotFail(t *testing.T) {
	var hits atomic.Int32
	srv := idempotentServer(&hits)
	defer srv.Close()

	fp, err := httpx.StartFaultProxy(srv.URL, httpx.FaultLostResponse)
	if err != nil {
		t.Fatal(err)
	}
	defer fp.Close()

	r := newRunner(fp.URL(), `{"item_id":42}`)
	r.Concurrency = 4
	r.Settle = 10 * time.Millisecond
	r.ReplayTimeout = time.Second

	res, err := r.runConcurrentTrials(context.Background(), config.DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if fp.Dropped() != 1 {
		t.Fatalf("proxy dropped %d responses, want exactly 1", fp.Dropped())
	}
	if res.Status != models.StatusInconclusive {
		t.Fatalf("status = %s (%s): a lost response must be INCONCLUSIVE", res.Status, res.Detail)
	}
	if res.Transport != 1 {
		t.Errorf("transport failures = %d, want 1", res.Transport)
	}
	if res.Logical != 1 {
		t.Errorf("logical = %d: observed responses agreed on one result", res.Logical)
	}
	if res.Status == models.StatusFail {
		t.Error("a dropped response must never be reported as divergence")
	}
	// 4 burst (1 dropped) + 1 replay that retries the same key.
	if res.Requests != r.Concurrency+1 {
		t.Errorf("requests = %d, want %d", res.Requests, r.Concurrency+1)
	}
	// The server processed every request, including the one whose answer
	// was lost: 5 hits.
	if hits.Load() != int32(r.Concurrency+1) {
		t.Errorf("upstream hits = %d, want %d", hits.Load(), r.Concurrency+1)
	}
	// The replay must still have confirmed the stored logical result: the
	// phase chain stays visible even though the verdict is inconclusive.
	phases := res.ActionPhases
	if len(phases) < 3 {
		t.Fatalf("expected BURST/SETTLE/REPLAY phases, got %v", phases)
	}
}

// The same endpoint without the fault must converge: proves the INCONCLUSIVE
// above came from the drop, not from the endpoint or the proxy itself.
func TestProxyWithoutFaultConverges(t *testing.T) {
	var hits atomic.Int32
	srv := idempotentServer(&hits)
	defer srv.Close()

	fp, err := httpx.StartFaultProxy(srv.URL, httpx.FaultNone)
	if err != nil {
		t.Fatal(err)
	}
	defer fp.Close()

	r := newRunner(fp.URL(), `{"item_id":42}`)
	r.Concurrency = 4
	r.Settle = 10 * time.Millisecond
	r.ReplayTimeout = time.Second

	res, err := r.runConcurrentTrials(context.Background(), config.DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != models.StatusPass {
		t.Fatalf("status = %s (%s): proxy without a fault must not change the verdict", res.Status, res.Detail)
	}
	if fp.Dropped() != 0 {
		t.Errorf("dropped = %d, want 0", fp.Dropped())
	}
}
