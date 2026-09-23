package engine

import (
	"strings"
	"testing"

	"github.com/hyukvoid/idemcheck/internal/config"
	"github.com/hyukvoid/idemcheck/internal/fingerprint"
	"github.com/hyukvoid/idemcheck/internal/models"
)

// group builds an evidence group for a synthetic response: one fingerprint
// with the given status and canonical (already-normalized) body.
func group(status int, canonical string, count int) Group {
	return Group{
		Fingerprint: fingerprint.Fingerprint{
			Status: status,
			Body:   fingerprint.NormalizedBody{IsJSON: true, Canonical: []byte(canonical)},
		},
		Count: count,
	}
}

func result(groups ...Group) *CheckResult {
	res := &CheckResult{EvidenceFields: map[string]map[string]any{}}
	res.Groups = groups
	res.Unique = len(groups)
	for _, g := range groups {
		res.Observed += g.Count
		res.Requests += g.Count
	}
	return res
}

func safePolicy(t *testing.T) config.Policy {
	t.Helper()
	p, err := config.ResolvePolicy(config.PolicySafeRetry, nil)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func strictPolicy(t *testing.T) config.Policy {
	t.Helper()
	p, err := config.ResolvePolicy(config.PolicyStrictReplay, nil)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// The core false-positive fix: identical bodies that differ only in status
// class (201 vs 200) or headers have converged — one logical result.
func TestEvaluateStatusCollapseConverges(t *testing.T) {
	res := result(group(201, `{"id":"ord_1"}`, 6), group(200, `{"id":"ord_1"}`, 4))
	evaluateSameKey(res, safePolicy(t), nil)
	if res.Status != models.StatusPass {
		t.Fatalf("201/200 with the same body must PASS, got %s (%s)", res.Status, res.Detail)
	}
	if res.Logical != 1 {
		t.Errorf("logical results = %d, want 1", res.Logical)
	}
}

// Divergent success bodies are a concrete failure, regardless of profile.
func TestEvaluateDivergentBodiesFail(t *testing.T) {
	res := result(group(201, `{"id":"ord_1"}`, 4), group(201, `{"id":"ord_2"}`, 4), group(201, `{"id":"ord_3"}`, 2))
	evaluateSameKey(res, safePolicy(t), nil)
	if res.Status != models.StatusFail {
		t.Fatalf("three distinct bodies must FAIL, got %s (%s)", res.Status, res.Detail)
	}
	if res.Logical != 3 {
		t.Errorf("logical results = %d, want 3", res.Logical)
	}
}

// Mixed statuses do not auto-fail when the bodies agree.
func TestEvaluateMixedStatusSameBody(t *testing.T) {
	res := result(group(201, `{"id":"ord_1"}`, 1), group(409, `{"error":"conflict"}`, 2))
	evaluateSameKey(res, safePolicy(t), nil)
	// 409 is a tolerated transient, but convergence needs replay
	// confirmation: uncertainty must not become a pass.
	if res.Status != models.StatusInconclusive {
		t.Fatalf("201 + tolerated 409 must be INCONCLUSIVE (replay pending), got %s (%s)", res.Status, res.Detail)
	}
}

func TestEvaluateTransientsOnlyInconclusive(t *testing.T) {
	res := result(group(429, `{"error":"slow down"}`, 3))
	evaluateSameKey(res, safePolicy(t), nil)
	if res.Status != models.StatusInconclusive {
		t.Fatalf("transients only must be INCONCLUSIVE, got %s (%s)", res.Status, res.Detail)
	}
}

func TestEvaluateUnknownStatusInconclusive(t *testing.T) {
	res := result(group(200, `{"id":"ord_1"}`, 1), group(500, `{"error":"boom"}`, 1))
	evaluateSameKey(res, safePolicy(t), nil)
	if res.Status != models.StatusInconclusive {
		t.Fatalf("200 + 500 (not in policy) must be INCONCLUSIVE, got %s (%s)", res.Status, res.Detail)
	}
}

// Under strict-replay a tolerated-under-safe 409 is unknown: still
// inconclusive, never a pass, and the detail names the profile.
func TestEvaluateStrictReplayTreats409AsUnknown(t *testing.T) {
	res := result(group(201, `{"id":"ord_1"}`, 1), group(409, `{"error":"conflict"}`, 1))
	evaluateSameKey(res, strictPolicy(t), nil)
	if res.Status != models.StatusInconclusive {
		t.Fatalf("strict-replay with 409 must be INCONCLUSIVE, got %s (%s)", res.Status, res.Detail)
	}
	if res.Detail == "" {
		t.Error("inconclusive detail must explain why")
	}
}

func TestEvaluateNothingObservedIsError(t *testing.T) {
	res := &CheckResult{Requests: 3, FirstError: "connection refused"}
	evaluateSameKey(res, safePolicy(t), nil)
	if res.Status != models.StatusError {
		t.Fatalf("zero observations must be ERROR, got %s", res.Status)
	}
}

func TestEvaluateOversizeInconclusive(t *testing.T) {
	res := &CheckResult{Requests: 3, Observed: 3, Oversize: 3}
	evaluateSameKey(res, safePolicy(t), nil)
	if res.Status != models.StatusInconclusive {
		t.Fatalf("oversize bodies must be INCONCLUSIVE, got %s (%s)", res.Status, res.Detail)
	}
}

func TestEvaluatePartialTransportInconclusive(t *testing.T) {
	res := result(group(200, `{"id":"ord_1"}`, 2))
	res.Transport = 1
	res.Requests = 3
	res.FirstError = "connection reset"
	evaluateSameKey(res, safePolicy(t), nil)
	if res.Status != models.StatusInconclusive {
		t.Fatalf("partial transport failure must be INCONCLUSIVE, got %s (%s)", res.Status, res.Detail)
	}
}

// A 201 plus tolerated 409s converges only when the REPLAY phase answered
// with a logical result: that confirmation is what lifts the doubt.
func TestEvaluateTransientConfirmedByReplayPasses(t *testing.T) {
	res := result(group(201, `{"id":"ord_1"}`, 1), group(409, `{"error":"conflict"}`, 2))
	evaluateSameKey(res, safePolicy(t), &ReplayInfo{Attempts: 2, Status: 200, Confirmed: true})
	if res.Status != models.StatusPass {
		t.Fatalf("replay-confirmed convergence must PASS, got %s (%s)", res.Status, res.Detail)
	}
	if !strings.Contains(res.Detail, "confirmed") {
		t.Errorf("detail must mention replay confirmation: %s", res.Detail)
	}
	if res.Logical != 1 {
		t.Errorf("logical = %d, want 1", res.Logical)
	}
}

// A replay that spent its budget still answering transient must not turn
// the check into a pass.
func TestEvaluateReplayExhaustedStaysInconclusive(t *testing.T) {
	res := result(group(201, `{"id":"ord_1"}`, 1), group(429, `{"error":"slow down"}`, 2))
	evaluateSameKey(res, safePolicy(t), &ReplayInfo{Attempts: replayMaxAttempts, Status: 429, Exhausted: true})
	if res.Status != models.StatusInconclusive {
		t.Fatalf("exhausted replay must be INCONCLUSIVE, got %s (%s)", res.Status, res.Detail)
	}
	if !strings.Contains(res.Detail, "replay") {
		t.Errorf("detail must explain the replay outcome: %s", res.Detail)
	}
}

// Transients only, replay never produced a logical result: still
// inconclusive (nothing to compare), never a pass.
func TestEvaluateTransientsOnlyReplayUnconfirmed(t *testing.T) {
	res := result(group(409, `{"error":"conflict"}`, 3))
	evaluateSameKey(res, safePolicy(t), &ReplayInfo{Attempts: 3, Status: 409})
	if res.Status != models.StatusInconclusive {
		t.Fatalf("unconfirmed transients must be INCONCLUSIVE, got %s (%s)", res.Status, res.Detail)
	}
}

// Clean burst plus an agreeing replay: one logical result, noted.
func TestEvaluateReplayAgreedNotesConvergence(t *testing.T) {
	res := result(group(201, `{"id":"ord_1"}`, 10), group(200, `{"id":"ord_1"}`, 1))
	evaluateSameKey(res, safePolicy(t), &ReplayInfo{Attempts: 1, Status: 200, Confirmed: true})
	if res.Status != models.StatusPass {
		t.Fatalf("burst + agreeing replay must PASS, got %s (%s)", res.Status, res.Detail)
	}
	if !strings.Contains(res.Detail, "replay agreed") {
		t.Errorf("detail should note the replay agreed: %s", res.Detail)
	}
}

// The replay itself can expose divergence: a second success body from the
// stored result is a concrete failure, not a pass.
func TestEvaluateReplayDivergenceFails(t *testing.T) {
	res := result(group(201, `{"id":"ord_1"}`, 10), group(200, `{"id":"ord_2"}`, 1))
	evaluateSameKey(res, safePolicy(t), &ReplayInfo{Attempts: 1, Status: 200, Confirmed: true})
	if res.Status != models.StatusFail {
		t.Fatalf("divergent replay body must FAIL, got %s (%s)", res.Status, res.Detail)
	}
	if res.Logical != 2 {
		t.Errorf("logical = %d, want 2", res.Logical)
	}
}
