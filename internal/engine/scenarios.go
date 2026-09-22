package engine

import (
	"context"
	"encoding/json"
	"fmt"
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

// Runner executes the full v0.1 check matrix against one target.
type Runner struct {
	Client      *httpx.Client
	Spec        httpx.Spec
	FPOpts      fingerprint.Options
	BaseKey     string // caller-provided or generated; never sent as-is
	Repeat      int
	Concurrency int
	// OnCheck is invoked after each check completes (live terminal output).
	OnCheck func(CheckResult)
}

// Run executes all checks in order and returns them. It stops early when the
// baseline request is rejected (>=400): that indicates a misconfigured test,
// not an idempotency verdict, and further requests would be wasted noise.
func (r *Runner) Run(ctx context.Context) ([]CheckResult, error) {
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
	evalDuplicate(seq2)

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
		evalDuplicate(seqN)
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
	evalDuplicate(conc)
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

// evalDuplicate assigns pass/fail/error to a same-key check: identical
// semantic responses pass; more than one distinct response is a violation.
func evalDuplicate(res *CheckResult) {
	if res.Observed == 0 {
		res.Status = models.StatusError
		res.Detail = "no responses received: " + res.FirstError
		return
	}
	detail := fmt.Sprintf("%d unique semantic responses observed", res.Unique)
	failed := res.Requests - res.Observed
	if failed > 0 {
		detail += fmt.Sprintf("; %d/%d requests failed: %s", failed, res.Requests, res.FirstError)
	}
	res.Detail = detail

	switch {
	case res.Unique > 1:
		res.Status = models.StatusFail
	case failed > 0:
		// All observed responses matched, but some requests never completed:
		// inconclusive rather than a clean pass.
		res.Status = models.StatusWarn
	default:
		res.Status = models.StatusPass
	}
}

// runPayloadConflict sends the original payload, then a modified payload,
// under the SAME key, and classifies the endpoint's behavior without
// imposing any universal HTTP contract (APIs differ by design).
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
		res.Status = models.StatusError
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

	switch {
	case ocB.StatusCode >= 400 && ocA.StatusCode < 400:
		res.Status = models.StatusPass
		res.Detail = fmt.Sprintf("payload conflict rejected: original HTTP %d, modified payload HTTP %d (%s)",
			ocA.StatusCode, ocB.StatusCode, marker)
	case fpA.Value == fpB.Value:
		res.Status = models.StatusPass
		res.Detail = fmt.Sprintf("modified payload (%s) replayed the original response (HTTP %d)", marker, ocA.StatusCode)
	default:
		res.Status = models.StatusWarn
		res.Detail = fmt.Sprintf("same idempotency key accepted for different payloads: original HTTP %d, modified payload HTTP %d produced a different response",
			ocA.StatusCode, ocB.StatusCode)
	}
	return res, nil
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
		res.Status = models.StatusWarn
		res.Detail = "identical responses for different keys: either the endpoint deduplicates by payload, or responses contain no distinguishing field"
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
