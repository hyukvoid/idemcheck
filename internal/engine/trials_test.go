package engine

import (
	"strings"
	"testing"

	"github.com/hyukvoid/idemcheck/internal/models"
)

// mkTrial builds one already-evaluated trial result.
func mkTrial(status models.CheckStatus, detail string, requests int, fp string) CheckResult {
	res := CheckResult{
		ID: ScConcurrent, Name: "Concurrent retry ×4",
		Status: status, Detail: detail,
		Requests: requests, Observed: requests,
		Unique: 1, Logical: 1, Trials: 1,
	}
	if fp != "" {
		res.Groups = []Group{group(201, fp, requests)}
	}
	return res
}

// Any failing trial makes the aggregate FAIL, and the wording must report
// the trial ratio, never a single burst.
func TestAggregateTrialsRaceWording(t *testing.T) {
	trials := []CheckResult{
		mkTrial(models.StatusPass, "4 requests converged on 1 logical result", 5, `{"id":"a"}`),
		mkTrial(models.StatusFail, "2 distinct logical results", 5, `{"id":"b"}`),
		mkTrial(models.StatusPass, "4 requests converged on 1 logical result", 5, `{"id":"a"}`),
		mkTrial(models.StatusFail, "2 distinct logical results", 5, `{"id":"c"}`),
		mkTrial(models.StatusPass, "4 requests converged on 1 logical result", 5, `{"id":"a"}`),
	}
	agg := aggregateTrials(trials, 4, 0)

	if agg.Status != models.StatusFail {
		t.Fatalf("aggregate = %s, want FAIL", agg.Status)
	}
	if want := "Concurrency race detected in 2 / 5 trials"; agg.Detail != want {
		t.Errorf("detail = %q, want %q", agg.Detail, want)
	}
	if agg.Trials != 5 {
		t.Errorf("trials = %d, want 5", agg.Trials)
	}
	// Counts sum across trials: 5 trials × 5 requests.
	if agg.Requests != 25 || agg.Observed != 25 {
		t.Errorf("requests/observed = %d/%d, want 25/25", agg.Requests, agg.Observed)
	}
	// Evidence comes from the first decisive (FAIL) trial.
	if len(agg.Groups) != 1 {
		t.Fatalf("evidence groups not carried over: %+v", agg.Groups)
	}
	if got := string(agg.Groups[0].Fingerprint.Body.Canonical); got != `{"id":"b"}` {
		t.Errorf("evidence body = %s, want the failing trial's body {\"id\":\"b\"}", got)
	}
	if len(agg.ActionPhases) == 0 || !strings.HasPrefix(agg.ActionPhases[0], "BURST:") {
		t.Errorf("aggregate phases missing BURST: %v", agg.ActionPhases)
	}
}

// All-clear runs must not overclaim: absence of an observed race is
// explicitly not proof.
func TestAggregateTrialsAllPassWording(t *testing.T) {
	trials := make([]CheckResult, 5)
	for i := range trials {
		trials[i] = mkTrial(models.StatusPass, "converged", 5, `{"id":"a"}`)
	}
	agg := aggregateTrials(trials, 4, 0)
	if agg.Status != models.StatusPass {
		t.Fatalf("aggregate = %s, want PASS", agg.Status)
	}
	if !strings.Contains(agg.Detail, "No observable race in 5 trials") {
		t.Errorf("detail = %q", agg.Detail)
	}
	if !strings.Contains(agg.Detail, "not proof") {
		t.Errorf("pass wording must avoid overclaiming: %q", agg.Detail)
	}
}

// Uncertainty must not aggregate into a pass: one inconclusive trial keeps
// the whole check inconclusive when nothing failed.
func TestAggregateTrialsInconclusiveNeverPasses(t *testing.T) {
	trials := []CheckResult{
		mkTrial(models.StatusPass, "converged", 5, `{"id":"a"}`),
		mkTrial(models.StatusInconclusive, "transients need replay", 5, ""),
		mkTrial(models.StatusPass, "converged", 5, `{"id":"a"}`),
	}
	agg := aggregateTrials(trials, 4, 0)
	if agg.Status != models.StatusInconclusive {
		t.Fatalf("aggregate = %s, want INCONCLUSIVE", agg.Status)
	}
	if !strings.Contains(agg.Detail, "1 / 3 trials") {
		t.Errorf("detail = %q", agg.Detail)
	}
}

// Execution failures outrank inconclusive, but a proven race outranks both.
func TestAggregateTrialsRankOrder(t *testing.T) {
	rank := []struct {
		status models.CheckStatus
		want   models.CheckStatus
	}{
		{models.StatusError, models.StatusError},
		{models.StatusFail, models.StatusFail},
	}
	for _, c := range rank {
		trials := []CheckResult{
			mkTrial(c.status, "decisive", 5, ""),
			mkTrial(models.StatusInconclusive, "unsure", 5, ""),
			mkTrial(models.StatusPass, "converged", 5, `{"id":"a"}`),
		}
		agg := aggregateTrials(trials, 4, 0)
		if agg.Status != c.want {
			t.Errorf("with %s present: aggregate = %s, want %s", c.status, agg.Status, c.want)
		}
	}
}

func TestAggregateTrialsEmptyIsError(t *testing.T) {
	agg := aggregateTrials(nil, 4, 0)
	if agg.Status != models.StatusError {
		t.Fatalf("empty trials must be ERROR, got %s", agg.Status)
	}
}

// The aggregate reports what ran: one burst line per trial count and a
// replay summary across trials.
func TestAggregateTrialsPhaseLines(t *testing.T) {
	trials := []CheckResult{
		mkTrial(models.StatusPass, "converged", 5, `{"id":"a"}`),
		mkTrial(models.StatusFail, "diverged", 5, `{"id":"b"}`),
	}
	for i := range trials {
		trials[i].Replay = &ReplayInfo{Attempts: 1, Status: 200, Confirmed: true}
	}
	agg := aggregateTrials(trials, 4, 0)
	joined := strings.Join(agg.ActionPhases, "\n")
	if !strings.Contains(joined, "4 requests × 2 trials") {
		t.Errorf("BURST phase missing trial count:\n%s", joined)
	}
	if !strings.Contains(joined, "REPLAY: 2/2 confirmed") {
		t.Errorf("REPLAY phase missing confirmation summary:\n%s", joined)
	}
}
