package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hyukvoid/idemcheck/internal/httpx"
	"github.com/hyukvoid/idemcheck/internal/models"
)

// want is one expected verdict inside the reference matrix.
type want struct {
	id     string
	status models.CheckStatus
}

// fixture is a reference behavior with the exact verdicts it must produce.
// The pair fp/fn makes the trade-off explicit:
//
//	fp — the false positive this fixture guards against (a wrong FAIL, or a
//	     wrong PASS reported with confidence)
//	fn — a documented false negative or limitation the tool does NOT claim
//	     to solve (the black-box boundary)
type fixture struct {
	id    string
	title string
	// build returns the target URL, a cleanup func, and optional extra
	// assertions that run after the matrix executed.
	build func(t *testing.T) (string, func(), func(t *testing.T))
	want  []want
	fp    string
	fn    string
}

const (
	idSeq2       = ScSeq2
	idSeqN       = ScSeqN
	idConcurrent = ScConcurrent
	idPayload    = ScPayload
	idDistinct   = ScDistinct
)

// writeJSON replies with a status and a raw body.
func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

// correctStore implements the safe contract: one stored logical result per
// key, 409 when the key is reused with a changed payload, and distinct ids
// for distinct keys. firstStatus/statusShift let a fixture answer the first
// request with 201 and replays with 200 (identical body).
func correctStore(shiftStatus bool) http.HandlerFunc {
	var mu sync.Mutex
	stored := map[string]string{}
	var next int32
	return func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		key := r.Header.Get("Idempotency-Key")
		variant := containsVariant(raw)

		mu.Lock()
		body, ok := stored[key]
		mu.Unlock()

		if ok && variant {
			writeJSON(w, http.StatusConflict, `{"error":"idempotency key reused with a different payload"}`)
			return
		}
		if !ok {
			mu.Lock()
			if again, exists := stored[key]; exists {
				body, ok = again, true
			} else {
				next++
				body = fmt.Sprintf(`{"order_id":%d}`, next)
				stored[key] = body
			}
			mu.Unlock()
		}
		if ok && shiftStatus {
			writeJSON(w, http.StatusOK, body)
			return
		}
		writeJSON(w, http.StatusCreated, body)
	}
}

func containsVariant(raw []byte) bool {
	return bytes.Contains(raw, []byte(`"idemcheck_variant"`)) ||
		bytes.Contains(raw, []byte("#idemcheck-variant"))
}

// referenceFixtures is the A–J matrix. Verdicts here are the contract the
// tool advertises in docs/TEST_MATRIX.md; if a refactor changes one of
// them, the docs and the code are wrong together and this test says so.
var referenceFixtures = []fixture{
	{
		id:    "A",
		title: "correct idempotent store",
		build: func(t *testing.T) (string, func(), func(t *testing.T)) {
			srv := httptest.NewServer(correctStore(false))
			return srv.URL, srv.Close, func(*testing.T) {}
		},
		want: []want{
			{idSeq2, models.StatusPass}, {idSeqN, models.StatusPass},
			{idConcurrent, models.StatusPass}, {idPayload, models.StatusPass},
			{idDistinct, models.StatusPass},
		},
		fp: "no check may fail a correct endpoint",
		fn: "only response identity is observed",
	},
	{
		// 201 for the first answer, 200 for replays, SAME body: a correct
		// endpoint must not be failed for a status-class difference.
		id:    "B",
		title: "status class shifts (201 then 200, same body)",
		build: func(t *testing.T) (string, func(), func(t *testing.T)) {
			srv := httptest.NewServer(correctStore(true))
			return srv.URL, srv.Close, func(*testing.T) {}
		},
		want: []want{
			{idSeq2, models.StatusPass}, {idSeqN, models.StatusPass},
			{idConcurrent, models.StatusPass}, {idPayload, models.StatusPass},
			{idDistinct, models.StatusPass},
		},
		fp: "201 vs 200 with an identical body must not be reported as divergence",
		fn: "headers are evidence only",
	},
	{
		// The demo bug: duplicates LOOK idempotent one after another, but a
		// concurrent burst answers each request with its own id.
		id:    "C",
		title: "non-idempotent race (check-then-act, no lock)",
		build: func(t *testing.T) (string, func(), func(t *testing.T)) {
			var mu sync.Mutex
			stored := map[string]string{}
			var next atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				key := r.Header.Get("Idempotency-Key")
				mu.Lock()
				body, ok := stored[key]
				mu.Unlock()
				if !ok {
					// The TOCTOU window: everyone reads "not stored",
					// everyone creates, everyone answers its OWN id.
					time.Sleep(20 * time.Millisecond)
					mine := fmt.Sprintf(`{"order_id":%d}`, next.Add(1))
					mu.Lock()
					if _, exists := stored[key]; !exists {
						stored[key] = mine
					}
					mu.Unlock()
					body = mine
				}
				writeJSON(w, http.StatusCreated, body)
			}))
			return srv.URL, srv.Close, func(*testing.T) {}
		},
		want: []want{
			{idSeq2, models.StatusPass}, {idSeqN, models.StatusPass},
			{idConcurrent, models.StatusFail}, {idPayload, models.StatusPass},
			{idDistinct, models.StatusPass},
		},
		fp: "sequential success must not hide a concurrent race (no false PASS)",
		fn: "a race that never overlaps in time stays invisible",
	},
	{
		// Correct store, but every response carries regenerated volatile
		// fields: normalization must ignore them, not fail the endpoint.
		id:    "D",
		title: "volatile envelope (request_id/timestamp/trace_id change per response)",
		build: func(t *testing.T) (string, func(), func(t *testing.T)) {
			var mu sync.Mutex
			stored := map[string]string{}
			var next int32
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				key := r.Header.Get("Idempotency-Key")
				mu.Lock()
				id, ok := stored[key]
				if !ok {
					next++
					id = fmt.Sprintf("%d", next)
					stored[key] = id
				}
				mu.Unlock()
				calls.Add(1)
				writeJSON(w, http.StatusCreated, fmt.Sprintf(
					`{"order_id":%s,"request_id":"rq-%d","timestamp":%d,"trace_id":"tr-%d"}`,
					id, calls.Load(), time.Now().UnixNano(), calls.Load()))
			}))
			return srv.URL, srv.Close, func(*testing.T) {}
		},
		want: []want{
			{idSeq2, models.StatusPass}, {idSeqN, models.StatusPass},
			{idConcurrent, models.StatusPass}, {idPayload, models.StatusPass},
			{idDistinct, models.StatusPass},
		},
		fp: "volatile per-response noise must not be reported as divergence",
		fn: "volatile keys are ignored by name; business fields are not",
	},
	{
		// Dedup keyed by (idempotency key + payload): a second payload
		// creates a second resource under the SAME key.
		id:    "E",
		title: "payload conflict accepted (key stored without payload)",
		build: func(t *testing.T) (string, func(), func(t *testing.T)) {
			var mu sync.Mutex
			stored := map[string]string{}
			var next int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				sum := sha256.Sum256(raw)
				k := r.Header.Get("Idempotency-Key") + "|" + hex.EncodeToString(sum[:])
				mu.Lock()
				body, ok := stored[k]
				if !ok {
					next++
					body = fmt.Sprintf(`{"order_id":%d}`, next)
					stored[k] = body
				}
				mu.Unlock()
				writeJSON(w, http.StatusCreated, body)
			}))
			return srv.URL, srv.Close, func(*testing.T) {}
		},
		want: []want{
			{idSeq2, models.StatusPass}, {idSeqN, models.StatusPass},
			{idConcurrent, models.StatusPass}, {idPayload, models.StatusFail},
			{idDistinct, models.StatusPass},
		},
		fp: "one key yielding two logical results for two payloads must FAIL",
		fn: "only the tested payload pair is covered",
	},
	{
		// Every answer is byte-identical, for every key and payload: races
		// cannot be observed from responses at all.
		id:    "F",
		title: "constant responder (responses carry no distinguishing field)",
		build: func(t *testing.T) (string, func(), func(t *testing.T)) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, http.StatusOK, `{"ok":true}`)
			}))
			return srv.URL, srv.Close, func(*testing.T) {}
		},
		want: []want{
			{idSeq2, models.StatusPass}, {idSeqN, models.StatusPass},
			{idConcurrent, models.StatusPass}, {idPayload, models.StatusPass},
			{idDistinct, models.StatusInconclusive},
		},
		fp: "the control check must not report PASS when it cannot tell keys apart",
		fn: "constant responses make races unobservable; nothing is proven",
	},
	{
		// The duplicate (second) request per key is rate limited once.
		// Convergence is only reachable through replay confirmation.
		id:    "G",
		title: "rate limited duplicate (429 on the second request per key)",
		build: func(t *testing.T) (string, func(), func(t *testing.T)) {
			var mu sync.Mutex
			stored := map[string]string{}
			seen := map[string]int{}
			var next int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				key := r.Header.Get("Idempotency-Key")
				mu.Lock()
				seen[key]++
				n := seen[key]
				if n == 2 {
					mu.Unlock()
					writeJSON(w, http.StatusTooManyRequests, `{"error":"rate limited"}`)
					return
				}
				body, ok := stored[key]
				if !ok {
					next++
					body = fmt.Sprintf(`{"order_id":%d}`, next)
					stored[key] = body
				}
				mu.Unlock()
				writeJSON(w, http.StatusCreated, body)
			}))
			return srv.URL, srv.Close, func(*testing.T) {}
		},
		want: []want{
			{idSeq2, models.StatusPass}, {idSeqN, models.StatusPass},
			{idConcurrent, models.StatusPass}, {idPayload, models.StatusInconclusive},
			{idDistinct, models.StatusPass},
		},
		fp: "a tolerated transient never becomes PASS without replay confirmation, and never FAIL",
		fn: "the payload contract is not evaluable while the original request is rate limited",
	},
	{
		// The duplicate (second) request per key answers 500: outside every
		// policy profile, so it can only be inconclusive.
		id:    "H",
		title: "unknown status (500 on the second request per key)",
		build: func(t *testing.T) (string, func(), func(t *testing.T)) {
			var mu sync.Mutex
			stored := map[string]string{}
			seen := map[string]int{}
			var next int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				key := r.Header.Get("Idempotency-Key")
				mu.Lock()
				seen[key]++
				n := seen[key]
				if n == 2 {
					mu.Unlock()
					writeJSON(w, http.StatusInternalServerError, `{"error":"boom"}`)
					return
				}
				body, ok := stored[key]
				if !ok {
					next++
					body = fmt.Sprintf(`{"order_id":%d}`, next)
					stored[key] = body
				}
				mu.Unlock()
				writeJSON(w, http.StatusCreated, body)
			}))
			return srv.URL, srv.Close, func(*testing.T) {}
		},
		want: []want{
			{idSeq2, models.StatusInconclusive}, {idSeqN, models.StatusInconclusive},
			{idConcurrent, models.StatusInconclusive}, {idPayload, models.StatusInconclusive},
			{idDistinct, models.StatusPass},
		},
		fp: "5xx is never blindly accepted (no PASS) and never called a race (no FAIL)",
		fn: "an unclassified status ends the verdict at INCONCLUSIVE",
	},
	{
		// Correct store behind the lost-response proxy: the first completed
		// response is dropped after the server processed it.
		id:    "I",
		title: "lost response (fault injection drops the first completed answer)",
		build: func(t *testing.T) (string, func(), func(t *testing.T)) {
			up := httptest.NewServer(correctStore(false))
			fp, err := httpx.StartFaultProxy(up.URL, httpx.FaultLostResponse)
			if err != nil {
				t.Fatalf("start fault proxy: %v", err)
			}
			return fp.URL(), func() {
					fp.Close()
					up.Close()
				}, func(t *testing.T) {
					if fp.Dropped() != 1 {
						t.Errorf("proxy dropped %d responses, want exactly 1", fp.Dropped())
					}
				}
		},
		want: []want{
			{idSeq2, models.StatusInconclusive}, {idSeqN, models.StatusPass},
			{idConcurrent, models.StatusPass}, {idPayload, models.StatusPass},
			{idDistinct, models.StatusPass},
		},
		fp: "an unobserved request is never counted as convergence (no false PASS) nor as divergence (no false FAIL)",
		fn: "the lost response itself is unknowable: only the retry with the same key is observed",
	},
	{
		// The black-box boundary. Duplicates do extra internal work on
		// every request, yet every response is identical. The tool reports
		// PASS — it observes HTTP responses and claims nothing more.
		id:    "J",
		title: "hidden side effect (identical responses, extra internal work)",
		build: func(t *testing.T) (string, func(), func(t *testing.T)) {
			var mu sync.Mutex
			stored := map[string]string{}
			var next int32
			var hidden atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				key := r.Header.Get("Idempotency-Key")
				variant := containsVariant(raw)
				// The hidden work: runs on EVERY request, is never
				// visible in any response, and is deliberately NOT part
				// of any verdict.
				hidden.Add(1)

				mu.Lock()
				body, ok := stored[key]
				mu.Unlock()
				if ok && variant {
					writeJSON(w, http.StatusConflict, `{"error":"idempotency key reused with a different payload"}`)
					return
				}
				if !ok {
					mu.Lock()
					if again, exists := stored[key]; exists {
						body, ok = again, true
					} else {
						next++
						body = fmt.Sprintf(`{"order_id":%d}`, next)
						stored[key] = body
					}
					mu.Unlock()
				}
				writeJSON(w, http.StatusCreated, body)
			}))
			return srv.URL, srv.Close, func(t *testing.T) {
				// Documenting the boundary, NOT a detection claim: the
				// hidden counter advanced while the tool still reported a
				// PASS, because responses were converged.
				if hidden.Load() < 2 {
					t.Logf("hidden side effect counter = %d", hidden.Load())
				}
				t.Logf("black-box boundary: %d internal operations were never observed in any response",
					hidden.Load())
			}
		},
		want: []want{
			{idSeq2, models.StatusPass}, {idSeqN, models.StatusPass},
			{idConcurrent, models.StatusPass}, {idPayload, models.StatusPass},
			{idDistinct, models.StatusPass},
		},
		fp: "converged responses must be reported as converged",
		fn: "REMAINING FALSE NEGATIVE: internal work that never changes a response body is invisible to a black-box checker — IdemCheck does not detect hidden side effects and never claims to",
	},
}

// fixtureRunner configures one engine run against a fixture: fast settle,
// a bounded replay budget, and a burst large enough to overlap.
func fixtureRunner(url string) *Runner {
	r := newRunner(url, `{"item_id":42}`)
	r.Concurrency = 6
	r.Settle = 20 * time.Millisecond
	r.ReplayTimeout = time.Second
	return r
}

// TestReferenceFixturesMatrix executes every fixture A–J through the full
// check matrix and asserts each expected verdict. This is the executed form
// of docs/TEST_MATRIX.md: the false-positive/negative contract, regression
// tested end to end.
func TestReferenceFixturesMatrix(t *testing.T) {
	for _, fx := range referenceFixtures {
		fx := fx
		t.Run(fx.id+"_"+fx.title, func(t *testing.T) {
			url, cleanup, extra := fx.build(t)
			defer cleanup()

			results, err := fixtureRunner(url).Run(context.Background())
			if err != nil {
				t.Fatalf("run matrix: %v", err)
			}

			for _, w := range fx.want {
				got := findFixtureCheck(results, w.id)
				if got.Status != w.status {
					t.Errorf("fixture %s check %s = %s (%s), want %s\n  fp: %s\n  fn: %s",
						fx.id, w.id, got.Status, got.Detail, w.status, fx.fp, fx.fn)
				}
			}
			if len(fx.want) != 5 {
				t.Fatalf("fixture %s must declare all 5 checks, declared %d", fx.id, len(fx.want))
			}
			extra(t)
		})
	}
}

func findFixtureCheck(results []CheckResult, id string) CheckResult {
	for _, r := range results {
		if r.ID == id {
			return r
		}
	}
	return CheckResult{}
}

// Fixture J documents the tool's honest boundary: identical responses PASS
// even though the endpoint did extra work per request. The assertion is on
// what the tool reports — never on detecting the hidden work.
func TestFixtureJNeverClaimsHiddenSideEffectDetection(t *testing.T) {
	var fx *fixture
	for i := range referenceFixtures {
		if referenceFixtures[i].id == "J" {
			fx = &referenceFixtures[i]
		}
	}
	if fx == nil {
		t.Fatal("fixture J missing from the matrix")
	}
	url, cleanup, extra := fx.build(t)
	defer cleanup()

	results, err := fixtureRunner(url).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	extra(t)

	for _, r := range results {
		if r.Status != models.StatusPass {
			t.Errorf("fixture J check %s = %s (%s): converged responses must PASS",
				r.ID, r.Status, r.Detail)
		}
		// No verdict may imply knowledge the tool cannot have: it sees
		// HTTP responses, nothing else.
		lower := strings.ToLower(r.Detail)
		for _, claim := range []string{"hidden", "side effect", "internal work"} {
			if strings.Contains(lower, claim) {
				t.Errorf("check %s claims knowledge it cannot have: %q", r.ID, r.Detail)
			}
		}
	}
}
