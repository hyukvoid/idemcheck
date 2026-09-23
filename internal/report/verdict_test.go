package report

import (
	"strings"
	"testing"

	"github.com/hyukvoid/idemcheck/internal/config"
	"github.com/hyukvoid/idemcheck/internal/engine"
	"github.com/hyukvoid/idemcheck/internal/models"
)

func assembleWith(statuses map[string]models.CheckStatus, opts ...config.Options) *models.Result {
	var results []engine.CheckResult
	for id, st := range statuses {
		results = append(results, engine.CheckResult{ID: id, Name: id, Status: st, Requests: 2, Unique: 1, Logical: 1})
	}
	in := AssembleInput{Results: results, Options: config.Options{}}
	if len(opts) > 0 {
		in.Options = opts[0]
	}
	return Assemble(in)
}

// Exit precedence: FAIL(1) > ERROR(2) > INCONCLUSIVE(3) > PASS(0).
func TestExitPrecedence(t *testing.T) {
	cases := []struct {
		name     string
		statuses map[string]models.CheckStatus
		result   string
		exit     int
	}{
		{"all pass", map[string]models.CheckStatus{"a": models.StatusPass}, "PASS", 0},
		{"fail wins", map[string]models.CheckStatus{
			"a": models.StatusFail, "b": models.StatusError, "c": models.StatusInconclusive,
		}, "FAILED", 1},
		{"error over inconclusive", map[string]models.CheckStatus{
			"a": models.StatusError, "b": models.StatusInconclusive,
		}, "ERROR", 2},
		{"inconclusive never passes", map[string]models.CheckStatus{
			"a": models.StatusPass, "b": models.StatusInconclusive,
		}, "INCONCLUSIVE", 3},
		{"skip only", map[string]models.CheckStatus{"a": models.StatusSkip}, "PASS", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := assembleWith(c.statuses)
			if res.Summary.Result != c.result || res.Summary.ExitCode != c.exit {
				t.Errorf("got %s/%d, want %s/%d", res.Summary.Result, res.Summary.ExitCode, c.result, c.exit)
			}
		})
	}
}

func TestSummaryCountsInconclusiveNotWarned(t *testing.T) {
	res := assembleWith(map[string]models.CheckStatus{
		"a": models.StatusPass,
		"b": models.StatusFail,
		"c": models.StatusInconclusive,
		"d": models.StatusSkip,
	})
	if res.Summary.ChecksPassed != 1 || res.Summary.ChecksFailed != 1 ||
		res.Summary.ChecksInconclusive != 1 || res.Summary.ChecksSkipped != 1 {
		t.Errorf("counts = %+v", res.Summary)
	}
	if res.Summary.Policy != config.DefaultPolicyProfile {
		t.Errorf("policy = %q, want %q", res.Summary.Policy, config.DefaultPolicyProfile)
	}
}

func TestSummaryPolicyFromOptions(t *testing.T) {
	res := assembleWith(map[string]models.CheckStatus{"a": models.StatusPass},
		config.Options{Policy: config.PolicyStrictReplay})
	if res.Summary.Policy != config.PolicyStrictReplay {
		t.Errorf("policy = %q, want strict-replay", res.Summary.Policy)
	}
}

// The payload check failing must produce a payload_conflict violation, not
// a mislabeled sequential_mismatch.
func TestPayloadViolationType(t *testing.T) {
	res := assembleWith(map[string]models.CheckStatus{engine.ScPayload: models.StatusFail})
	if len(res.Violations) != 1 {
		t.Fatalf("violations = %d, want 1", len(res.Violations))
	}
	if res.Violations[0].Type != "payload_conflict" {
		t.Errorf("type = %q, want payload_conflict", res.Violations[0].Type)
	}
}

// Terminal rendering maps the JSON verdict strings to labels; FAIL (not
// FAILED) is what the user sees.
func TestResultLabel(t *testing.T) {
	cases := []struct {
		exit int
		want string
	}{
		{models.ExitPass, "PASS"},
		{models.ExitViolation, "FAIL"},
		{models.ExitFailure, "ERROR"},
		{models.ExitInconclusive, "INCONCLUSIVE"},
	}
	for _, c := range cases {
		if got := resultLabel(models.Summary{ExitCode: c.exit}); got != c.want {
			t.Errorf("resultLabel(%d) = %q, want %q", c.exit, got, c.want)
		}
	}
}

// The status column must render INCONCLUSIVE and no longer know WARN.
func TestStatusLabels(t *testing.T) {
	if got := statusLabel(models.StatusInconclusive); got != "INCONCLUSIVE" {
		t.Errorf("inconclusive label = %q", got)
	}
	if got := statusLabel(models.CheckStatus("warn")); got != "warn" {
		t.Errorf("retired warn status should render raw, got %q", got)
	}
}

// Inconclusive check lines must show their reason.
func TestTerminalShowsInconclusiveDetail(t *testing.T) {
	res := &models.Result{
		Summary: models.Summary{Result: "INCONCLUSIVE", ExitCode: models.ExitInconclusive},
		Checks: []models.Check{
			{ID: "a", Name: "Check", Status: models.StatusInconclusive, Detail: "need replay confirmation"},
		},
	}
	var sb strings.Builder
	Terminal(&sb, res)
	if !strings.Contains(sb.String(), "INCONCLUSIVE") || !strings.Contains(sb.String(), "need replay confirmation") {
		t.Errorf("terminal output missing inconclusive reason:\n%s", sb.String())
	}
}
