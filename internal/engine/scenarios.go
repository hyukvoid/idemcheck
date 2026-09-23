package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/hyukvoid/idemcheck/internal/config"
	"github.com/hyukvoid/idemcheck/internal/fingerprint"
	"github.com/hyukvoid/idemcheck/internal/httpx"
	"github.com/hyukvoid/idemcheck/internal/models"
)

// Scenario IDs (also used to derive per-scenario idempotency keys).
const (
	ScSeq2       = "seq2"
	ScSeqN       = "seqn"
	ScConcurrent = "concurrent"
	ScPayload    = "payload"
	ScDistinct   = "distinct"
)

// Runner executes the full check matrix against one target.
type Runner struct {
	Client      *httpx.Client
	Spec        httpx.Spec
	FPOpts      fingerprint.Options
	BaseKey     string // caller-provided or generated; never sent as-is
	Repeat      int
	Concurrency int
	// Policy decides verdict semantics (default safe-retry when unset).
	Policy config.Policy
	// OnCheck is invoked after each check completes (live terminal output).
	OnCheck func(CheckResult)
}

// Run executes all checks in order and returns them. It stops early when the
// baseline request is rejected (>=400): that indicates a misconfigured test,
// not an idempotency verdict, and further requests would be wasted noise.
func (r *Runner) Run(ctx context.Context) ([]CheckResult, error) {
	pol := r.Policy.WithDefaults()
	var results []CheckResult
	emit := func(res CheckResult) {
		if r.OnCheck != nil {
			r.OnCheck(res)
		}
		results = append(results, res)
	}

	// Check 1: sequential ×2.
	seq2, err := Sequential(ctx, r.Client, r.Spec, configKey(r.BaseKey, ScSeq2), 2, r.FPOpts)
	if err != nil {
		return nil, err
	}
	seq2.ID, seq2.Name = ScSeq2, "Sequential retry ×2"
	evaluateSameKey(seq2, pol)

	// A rejected baseline means the test itself is misconfigured; report it
	// as an execution failure rather than a misleading pass, and skip the
	// remaining checks instead of hammering a broken setup.
	baselineRejected := seq2.Observed > 0 && seq2.BaselineStatus >= 400
	if baselineRejected {
		seq2.Status = models.StatusError
		seq2.Detail = fmt.Sprintf("baseline request returned HTTP %d; fix --url/--body before testing idempotency", seq2.BaselineStatus)
	}
	emit(*seq2)

	if baselineRejected {
		for _, c := range []struct{ id, name string }{
			{ScSeqN, fmt.Sprintf("Sequential retry ×%d", r.Repeat)},
			{ScConcurrent, fmt.Sprintf("Concurrent retry ×%d", r.Concurrency)},
			{ScPayload, "Same key + changed payload"},
			{ScDistinct, "Different key + same payload"},
		} {
			emit(CheckResult{
				ID: c.id, Name: c.name, Status: models.StatusSkip, Detail: seq2.Detail,
			})
		}
		return results, nil
	}

	// Check 2: sequential stress with the same key.
	if r.Repeat > 2 {
		seqN, err := Sequential(ctx, r.Client, r.Spec, configKey(r.BaseKey, ScSeqN), r.Repeat, r.FPOpts)
		if err != nil {
			return results, err
		}
		seqN.ID, seqN.Name = ScSeqN, fmt.Sprintf("Sequential retry ×%d", r.Repeat)
		evaluateSameKey(seqN, pol)
		emit(*seqN)
	} else {
		emit(CheckResult{
			ID: ScSeqN, Name: fmt.Sprintf("Sequential retry ×%d", r.Repeat),
			Status: models.StatusSkip, Detail: "repeat=2 already covered by the ×2 check",
		})
	}

	// Check 3: the barrier-synchronized concurrent burst — the core test.
	conc, err := Concurrent(ctx, r.Client, r.Spec, configKey(r.BaseKey, ScConcurrent), r.Concurrency, r.FPOpts)
	if err != nil {
		return results, err
	}
	conc.ID, conc.Name = ScConcurrent, fmt.Sprintf("Concurrent retry ×%d", r.Concurrency)
	evaluateSameKey(conc, pol)
	emit(*conc)

	// Check 4: same key + modified payload (classified, never assumed).
	payloadRes, err := r.runPayloadConflict(ctx)
	if err != nil {
		return results, err
	}
	emit(*payloadRes)

	// Check 5: different key + same payload (control case).
	distinctRes, err := r.runDistinctKeys(ctx)
	if err != nil {
		return results, err
	}
	emit(*distinctRes)

	return results, nil
}

// evaluateSameKey assigns a verdict to a same-key check under the active
// policy. Verdict identity is the normalized response BODY of 2xx results:
// status class (201 vs 200) and headers collapse — they stay in the
// fingerprint evidence only. Rules, in order:
//
//  1. nothing observed -> ERROR (execution failure)
//  2. two or more distinct success bodies -> FAIL (concrete divergence)
//  3. oversize / transport / unknown statuses -> INCONCLUSIVE (partial evidence)
//  4. only transients, or transients alongside one success -> INCONCLUSIVE
//     (the REPLAY phase that confirms transient-tolerated convergence lands
//     with the phase machinery; until then these never report PASS)
//  5. exactly one success body, nothing else -> PASS
//
// Uncertainty is never turned into a pass.
func evaluateSameKey(res *CheckResult, pol config.Policy) {
	successBodies := map[string]bool{}
	transient, unknown := 0, 0
	unknownStatuses := map[int]bool{}
	for _, g := range res.Groups {
		st := g.Fingerprint.Status
		switch {
		case st >= 200 && st < 300:
			successBodies[string(g.Fingerprint.Body.Canonical)] = true
		case pol.IsTransient(st):
			transient += g.Count
		default:
			unknown += g.Count
			unknownStatuses[st] = true
		}
	}
	res.Logical = len(successBodies)

	switch {
	case res.Observed == 0:
		res.Status = models.StatusError
		res.Detail = "no responses received: " + res.FirstError

	case len(successBodies) >= 2:
		res.Status = models.StatusFail
		res.Detail = fmt.Sprintf("%d distinct logical results for one idempotency key: response bodies diverge", len(successBodies))

	case res.Oversize > 0:
		res.Status = models.StatusInconclusive
		res.Detail = fmt.Sprintf("%d of %d response bodies exceeded the read limit; bodies not compared (--max-body-bytes)",
			res.Oversize, res.Requests)

	case res.Transport > 0:
		res.Status = models.StatusInconclusive
		res.Detail = fmt.Sprintf("%d/%d requests failed before a verdict: %s", res.Transport, res.Requests, res.FirstError)

	case unknown > 0:
		statuses := make([]int, 0, len(unknownStatuses))
		for s := range unknownStatuses {
			statuses = append(statuses, s)
		}
		sort.Ints(statuses)
		res.Status = models.StatusInconclusive
		res.Detail = fmt.Sprintf("unhandled HTTP status %v outside policy %s; cannot classify", statuses, pol.Profile)

	case len(successBodies) == 0:
		// Transients only (policy accepted them, but nothing to compare).
		res.Status = models.StatusInconclusive
		res.Detail = fmt.Sprintf("%d transient response(s) under policy %s; no logical result to compare", transient, pol.Profile)

	case transient > 0:
		// One logical result plus policy-acceptable transients: pending
		// replay confirmation -> inconclusive, not a pass.
		res.Status = models.StatusInconclusive
		res.Detail = fmt.Sprintf("1 logical result observed but %d transient response(s) under policy %s need replay confirmation",
			transient, pol.Profile)

	default:
		// Exactly one logical result; every observed response was a 2xx
		// carrying that body.
		res.Status = models.StatusPass
		res.Detail = fmt.Sprintf("%d requests converged on 1 logical result", res.Observed)
	}
}

// runPayloadConflict sends the original payload, then a modified payload,
// under the SAME key, and classifies the endpoint's behavior as one of four
// outcomes without imposing any universal HTTP contract (APIs differ by
// design):
//
//	rejected          -> modified payload answered 4xx (except timeout-ish
//	                     408/425/429)            -> PASS
//	accepted-same     -> 2xx with the same logical result     -> PASS
//	accepted-different-> 2xx with a different logical result  -> FAIL
//	inconclusive      -> transient/unknown/transport outcomes -> INCONCLUSIVE
func (r *Runner) runPayloadConflict(ctx context.Context) (*CheckResult, error) {
	res := &CheckResult{
		ID: ScPayload, Name: "Same key + changed payload",
		EvidenceFields: map[string]map[string]any{},
	}
	if len(r.Spec.Body) == 0 {
		res.Status = models.StatusSkip
		res.Detail = "no request body to modify"
		return res, nil
	}

	key := configKey(r.BaseKey, ScPayload)
	modBody, marker := modifyBody(r.Spec.Body)

	modSpec := r.Spec
	modSpec.Body = modBody

	start := time.Now()
	ocA := r.Client.Do(ctx, r.Spec, key, 0)
	ocB := r.Client.Do(ctx, modSpec, key, 1)
	res.Duration = time.Since(start)

	outcomes := []httpx.Outcome{ocA, ocB}
	for _, oc := range outcomes {
		res.Requests++
		if oc.Err != nil && res.FirstError == "" {
			res.FirstError = httpx.Describe(oc.Err)
		}
	}
	if ocA.Err != nil || ocB.Err != nil {
		res.Observed = countObserved(outcomes)
		res.Status = models.StatusInconclusive
		res.Detail = "request failed: " + res.FirstError
		return res, nil
	}
	res.Observed = 2
	res.BaselineStatus = ocA.StatusCode

	fpA, err := fingerprint.Compute(toResponse(ocA), r.FPOpts)
	if err != nil {
		return nil, err
	}
	fpB, err := fingerprint.Compute(toResponse(ocB), r.FPOpts)
	if err != nil {
		return nil, err
	}
	res.Unique = 1
	res.Groups = []Group{{Fingerprint: fpA, Count: 1}}
	if fpA.Value != fpB.Value {
		res.Unique = 2
		res.Groups = append(res.Groups, Group{Fingerprint: fpB, Count: 1})
	}
	analyzeEvidence(res)

	sameBody := bytes.Equal(fpA.Body.Canonical, fpB.Body.Canonical)
	switch {
	case ocA.StatusCode < 200 || ocA.StatusCode >= 300:
		res.Status = models.StatusInconclusive
		res.Detail = fmt.Sprintf("original request returned HTTP %d; payload conflict not evaluable", ocA.StatusCode)

	case isRejectionStatus(ocB.StatusCode):
		res.Status = models.StatusPass
		res.Detail = fmt.Sprintf("payload conflict rejected: original HTTP %d, modified payload HTTP %d (%s)",
			ocA.StatusCode, ocB.StatusCode, marker)

	case ocB.StatusCode >= 200 && ocB.StatusCode < 300 && sameBody:
		res.Logical = 1
		res.Status = models.StatusPass
		res.Detail = fmt.Sprintf("modified payload (%s) replayed the original logical result (HTTP %d -> %d)",
			marker, ocA.StatusCode, ocB.StatusCode)

	case ocB.StatusCode >= 200 && ocB.StatusCode < 300:
		res.Logical = 2
		res.Status = models.StatusFail
		res.Detail = fmt.Sprintf("same idempotency key accepted for different payloads: original HTTP %d and modified payload HTTP %d returned different logical results (%s)",
			ocA.StatusCode, ocB.StatusCode, marker)

	default:
		// 1xx/3xx/408/425/429/5xx: cannot classify the contract.
		res.Status = models.StatusInconclusive
		res.Detail = fmt.Sprintf("modified payload returned HTTP %d (transient or unhandled); payload conflict not evaluable",
			ocB.StatusCode)
	}
	return res, nil
}

// isRejectionStatus reports whether B's status is a 4xx rejection of the
// modified payload. 408 (timeout), 425 (too early) and 429 (rate limit)
// are transport-ish conditions, not payload-conflict answers; 409 Conflict
// is the classic rejection and counts.
func isRejectionStatus(status int) bool {
	if status < 400 || status > 499 {
		return false
	}
	switch status {
	case 408, 425, 429:
		return false
	}
	return true
}

// runDistinctKeys is the control: two requests, different keys, same payload.
// A endpoint that distinguishes keys should produce two distinct responses.
func (r *Runner) runDistinctKeys(ctx context.Context) (*CheckResult, error) {
	res := &CheckResult{
		ID: ScDistinct, Name: "Different key + same payload",
		EvidenceFields: map[string]map[string]any{},
	}
	start := time.Now()
	ocA := r.Client.Do(ctx, r.Spec, configKey(r.BaseKey, ScDistinct+"1"), 0)
	ocB := r.Client.Do(ctx, r.Spec, configKey(r.BaseKey, ScDistinct+"2"), 1)
	res.Duration = time.Since(start)

	outcomes := []httpx.Outcome{ocA, ocB}
	res.Requests = 2
	for _, oc := range outcomes {
		if oc.Err != nil && res.FirstError == "" {
			res.FirstError = httpx.Describe(oc.Err)
		}
	}
	res.Observed = countObserved(outcomes)
	if res.Observed < 2 {
		res.Status = models.StatusError
		res.Detail = "request failed: " + res.FirstError
		return res, nil
	}
	res.BaselineStatus = ocA.StatusCode
	if ocA.StatusCode >= 400 || ocB.StatusCode >= 400 {
		res.Status = models.StatusError
		res.Detail = fmt.Sprintf("control request rejected: HTTP %d / HTTP %d", ocA.StatusCode, ocB.StatusCode)
		return res, nil
	}

	fpA, err := fingerprint.Compute(toResponse(ocA), r.FPOpts)
	if err != nil {
		return nil, err
	}
	fpB, err := fingerprint.Compute(toResponse(ocB), r.FPOpts)
	if err != nil {
		return nil, err
	}
	res.Unique = 1
	res.Groups = []Group{{Fingerprint: fpA, Count: 1}}
	if fpA.Value != fpB.Value {
		res.Unique = 2
		res.Groups = append(res.Groups, Group{Fingerprint: fpB, Count: 1})
	}
	analyzeEvidence(res)

	if res.Unique == 2 {
		res.Status = models.StatusPass
		res.Detail = "endpoint treats different keys as distinct requests: 2 unique semantic responses"
	} else {
		// Constant responses make races invisible: this run cannot prove
		// idempotency either way, so it must not report a pass.
		res.Status = models.StatusInconclusive
		res.Detail = "identical responses for different keys: either the endpoint deduplicates by payload, or responses contain no distinguishing field — a race could not be observed from this run"
	}
	return res, nil
}

// modifyBody derives the "changed payload" for Case B deterministically:
// JSON objects gain an "idemcheck_variant" field; other JSON gains an
// element; non-JSON bodies get a suffix marker.
func modifyBody(body []byte) (modified []byte, marker string) {
	var tree any
	if err := json.Unmarshal(body, &tree); err == nil {
		switch t := tree.(type) {
		case map[string]any:
			t["idemcheck_variant"] = 1
			out, err := json.Marshal(t)
			if err == nil {
				return out, `added "idemcheck_variant" field`
			}
		case []any:
			if b, err := json.Marshal(append(t, "idemcheck-variant")); err == nil {
				return b, "appended array element"
			}
		}
	}
	// Non-JSON: append a marker so the payload bytes certainly differ.
	mod := append(append([]byte{}, body...), []byte("#idemcheck-variant")...)
	return mod, "appended marker to raw body"
}

func countObserved(outcomes []httpx.Outcome) int {
	n := 0
	for _, oc := range outcomes {
		if oc.Err == nil {
			n++
		}
	}
	return n
}

func toResponse(oc httpx.Outcome) *fingerprint.Response {
	return &fingerprint.Response{
		StatusCode: oc.StatusCode,
		Header:     oc.Header,
		Body:       oc.Body,
	}
}

// configKey derives the scenario key via config.ScenarioKey: every scenario
// gets its own key so server-side state from check 1 can never leak into
// check 3; within a scenario all requests share the derived key exactly.
func configKey(base, scenario string) string {
	return config.ScenarioKey(base, scenario)
}
