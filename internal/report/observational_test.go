package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hyukvoid/idemcheck/internal/config"
	"github.com/hyukvoid/idemcheck/internal/engine"
	"github.com/hyukvoid/idemcheck/internal/models"
)

// render builds a result for the given concurrent-check status and renders
// both output formats. The concurrent check ID matters: the trial line is
// only shown when that check actually reached a verdict.
func render(t *testing.T, status models.CheckStatus, opts config.Options) (*models.Result, string, string) {
	t.Helper()
	res := Assemble(AssembleInput{
		Options: opts,
		Results: []engine.CheckResult{
			{ID: engine.ScConcurrent, Name: "Concurrent retry ×10", Status: status, Requests: 11, Unique: 1, Logical: 1, Trials: 1},
		},
	})
	var term strings.Builder
	Terminal(&term, res)
	var js bytes.Buffer
	if err := JSON(&js, res); err != nil {
		t.Fatalf("json: %v", err)
	}
	return res, term.String(), js.String()
}

// A. A normal safe run: the machine result stays PASS/0 while the terminal
// states the observational boundary instead of an unqualified "PASS".
func TestPassTerminalStatesObservationalScope(t *testing.T) {
	opts := config.Options{Trials: 1}
	res, term, js := render(t, models.StatusPass, opts)

	if res.Summary.Result != "PASS" || res.Summary.ExitCode != models.ExitPass {
		t.Fatalf("machine result = %s/%d, want PASS/0", res.Summary.Result, res.Summary.ExitCode)
	}
	for _, want := range []string{
		"Result:\nPASS (observed)\n",
		"No divergent HTTP result was observed within the configured HTTP-visible checks.",
		"It does not prove hidden downstream side effects were deduplicated.",
	} {
		if !strings.Contains(term, want) {
			t.Errorf("terminal missing %q:\n%s", want, term)
		}
	}
	// The scope note must not balloon into guarantee language.
	for _, bad := range []string{"guarantee", "statistical", "certainty"} {
		if strings.Contains(strings.ToLower(term), bad) {
			t.Errorf("terminal must not claim %q:\n%s", bad, term)
		}
	}
	// JSON keeps the stable machine strings: no terminal qualifiers leak in.
	if !strings.Contains(js, `"result": "PASS"`) || strings.Contains(js, "observed") {
		t.Errorf("json machine contract changed:\n%s", js)
	}
	var doc struct {
		Summary struct {
			Result   string `json:"result"`
			ExitCode int    `json:"exit_code"`
			Trials   int    `json:"trials"`
			Policy   string `json:"policy"`
		} `json:"summary"`
	}
	if err := json.Unmarshal([]byte(js), &doc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if doc.Summary.Result != "PASS" || doc.Summary.ExitCode != 0 || doc.Summary.Trials != 1 || doc.Summary.Policy == "" {
		t.Errorf("summary = %+v, want PASS/0 with trials and policy", doc.Summary)
	}
}

// B. PASS with active ignore paths: the terminal must expose the label and
// every exclusion verbatim — exclusions are never hidden on a pass.
func TestPassTerminalExposesExclusions(t *testing.T) {
	opts := config.Options{
		Trials:       1,
		IgnoreJSON:   []string{"$.request_id", "$.timestamp"},
		IgnoreHeader: []string{"x-custom-trace"},
	}
	res, term, _ := render(t, models.StatusPass, opts)

	if res.Summary.Result != "PASS" || res.Summary.ExitCode != 0 {
		t.Fatalf("machine result = %s/%d, want PASS/0", res.Summary.Result, res.Summary.ExitCode)
	}
	for _, want := range []string{
		"Result:\nPASS (observed, with exclusions)\n",
		"Ignored response fields:",
		"  $.request_id",
		"  $.timestamp",
		"Ignored headers:",
		"  x-custom-trace",
	} {
		if !strings.Contains(term, want) {
			t.Errorf("terminal missing %q:\n%s", want, term)
		}
	}
}

// C. Multiple trials: the trial count is visible in the human result.
func TestMultipleTrialsCountVisible(t *testing.T) {
	_, term, js := render(t, models.StatusPass, config.Options{Trials: 3})
	if !strings.Contains(term, "Concurrency trials: 3 observed") {
		t.Errorf("terminal missing trial count:\n%s", term)
	}
	if !strings.Contains(js, `"trials": 3`) {
		t.Errorf("json missing trials:\n%s", js)
	}
}

// D. Single trial: the count is visible but nothing implies statistical
// certainty from one observation.
func TestSingleTrialCountVisibleWithoutCertaintyClaims(t *testing.T) {
	_, term, _ := render(t, models.StatusPass, config.Options{Trials: 1})
	if !strings.Contains(term, "Concurrency trials: 1 observed") {
		t.Errorf("terminal missing trial count:\n%s", term)
	}
	for _, bad := range []string{"statistical", "guaranteed", "certainty", "proven"} {
		if strings.Contains(strings.ToLower(term), bad) {
			t.Errorf("single-trial output must not claim %q:\n%s", bad, term)
		}
	}
}

// E. A race stays an unqualified FAIL in the terminal and FAILED/1 in JSON.
func TestFailTerminalUnqualified(t *testing.T) {
	res, term, js := render(t, models.StatusFail, config.Options{Trials: 1})
	if res.Summary.Result != "FAILED" || res.Summary.ExitCode != 1 {
		t.Fatalf("machine result = %s/%d, want FAILED/1", res.Summary.Result, res.Summary.ExitCode)
	}
	if !strings.Contains(term, "Result:\nFAIL\n") || strings.Contains(term, "PASS") {
		t.Errorf("fail terminal output wrong:\n%s", term)
	}
	if !strings.Contains(js, `"result": "FAILED"`) || !strings.Contains(js, `"exit_code": 1`) {
		t.Errorf("json fail contract wrong:\n%s", js)
	}
}

// A burst that never reached a verdict (skipped check) must not claim an
// observed trial.
func TestNoTrialClaimWhenConcurrentSkipped(t *testing.T) {
	_, term, _ := render(t, models.StatusSkip, config.Options{Trials: 3})
	if strings.Contains(term, "Concurrency trials") {
		t.Errorf("skipped burst must not claim observed trials:\n%s", term)
	}
}
