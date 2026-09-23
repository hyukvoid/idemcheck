package report

import (
	"strings"
	"testing"

	"github.com/hyukvoid/idemcheck/internal/config"
	"github.com/hyukvoid/idemcheck/internal/engine"
	"github.com/hyukvoid/idemcheck/internal/models"
)

// The final report must distinguish what executed (BURST/SETTLE/REPLAY)
// from why it concluded (VERDICT), readable without the docs.
func TestTerminalRendersPhaseLines(t *testing.T) {
	res := &models.Result{
		Summary: models.Summary{Result: "PASS", ExitCode: models.ExitPass},
		Checks: []models.Check{
			{
				ID: engine.ScConcurrent, Name: "Concurrent retry ×10",
				Status: models.StatusPass,
				Detail: "11 requests converged on 1 logical result (replay agreed)",
				Phases: []string{
					"BURST: 10 requests released together (spread 0 ms)",
					"SETTLE: 250ms before replay",
					"REPLAY: 1 request -> HTTP 200 (confirmed a logical result)",
					"VERDICT: 11 requests converged on 1 logical result (replay agreed)",
				},
			},
		},
	}
	var sb strings.Builder
	Terminal(&sb, res)
	out := sb.String()
	for _, want := range []string{"BURST:", "SETTLE:", "REPLAY:", "VERDICT:"} {
		if !strings.Contains(out, want) {
			t.Errorf("terminal output missing %q:\n%s", want, out)
		}
	}
}

// Checks without recorded phases (skips, plain details) still show why they
// stopped short of a verdict.
func TestTerminalFallsBackToDetailLine(t *testing.T) {
	res := &models.Result{
		Summary: models.Summary{Result: "PASS", ExitCode: models.ExitPass},
		Checks: []models.Check{
			{ID: engine.ScSeqN, Name: "Sequential retry ×10", Status: models.StatusSkip,
				Detail: "repeat=2 already covered by the ×2 check"},
		},
	}
	var sb strings.Builder
	Terminal(&sb, res)
	if !strings.Contains(sb.String(), "repeat=2 already covered") {
		t.Errorf("skip detail missing:\n%s", sb.String())
	}
}

// A failing check renders its reasoning exactly once: as the VERDICT phase,
// not again inside the violation body.
func TestViolationDoesNotRepeatVerdictDetail(t *testing.T) {
	detail := "Concurrency race detected in 2 / 5 trials"
	res := &models.Result{
		Summary: models.Summary{Result: "FAILED", ExitCode: models.ExitViolation},
		Checks: []models.Check{
			{
				ID: engine.ScConcurrent, Name: "Concurrent retry ×10",
				Status: models.StatusFail, Detail: detail, Requests: 55, UniqueResponses: 2,
				Phases: []string{"BURST: 10 requests × 5 trials (isolated per-trial keys)", "VERDICT: " + detail},
			},
		},
		Violations: []models.Violation{
			{CheckID: engine.ScConcurrent, Type: "concurrent_race", Message: detail,
				DifferingFields: []string{"$.order_id"}},
		},
		Evidence: []models.EvidenceGroup{
			{CheckID: engine.ScConcurrent, Label: "A", Count: 33, Status: 201, Fields: map[string]any{"$.order_id": 1}},
			{CheckID: engine.ScConcurrent, Label: "B", Count: 22, Status: 201, Fields: map[string]any{"$.order_id": 2}},
		},
		Reproduce: &models.Reproduce{Command: "idemcheck test --url http://localhost/x --key k"},
	}
	var sb strings.Builder
	Terminal(&sb, res)
	out := sb.String()

	if n := strings.Count(out, detail); n != 2 {
		// Once as the violation message, once as the VERDICT phase line —
		// and never a third duplicate from the violation body.
		t.Errorf("detail appears %d times, want exactly 2 (message + verdict):\n%s", n, out)
	}
	for _, want := range []string{"RACE CONDITION DETECTED", "Fingerprint A", "Differing fields", "Re-run:"} {
		if !strings.Contains(out, want) {
			t.Errorf("violation body missing %q:\n%s", want, out)
		}
	}
}

// Multi-trial violations report the trial ratio as their message.
func TestViolationMessageUsesTrialRatio(t *testing.T) {
	res := Assemble(AssembleInput{
		Results: []engine.CheckResult{{
			ID: engine.ScConcurrent, Name: "Concurrent retry ×10",
			Status:   models.StatusFail,
			Requests: 55, Unique: 2, Logical: 2, Trials: 5,
			Detail: "Concurrency race detected in 2 / 5 trials",
		}},
		Options: config.Options{Trials: 5},
	})
	if len(res.Violations) != 1 {
		t.Fatalf("violations = %d, want 1", len(res.Violations))
	}
	v := res.Violations[0]
	if v.Type != "concurrent_race" {
		t.Errorf("type = %q", v.Type)
	}
	if v.Message != "Concurrency race detected in 2 / 5 trials" {
		t.Errorf("message = %q", v.Message)
	}
	if !strings.Contains(res.Reproduce.Command, "--trials 5") {
		t.Errorf("repro command must carry --trials: %s", res.Reproduce.Command)
	}
}
