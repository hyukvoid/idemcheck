// Package report renders results as human-readable terminal output or
// stable JSON.
package report

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/hyukvoid/idemcheck/internal/buildinfo"
	"github.com/hyukvoid/idemcheck/internal/models"
)

const ruleWidth = 34

// Terminal writes the human-readable report.
func Terminal(w io.Writer, res *models.Result) {
	fmt.Fprintf(w, "%s %s\n\n", models.ToolName, buildinfo.Display())
	fmt.Fprintf(w, "Target\n%s %s\n\n", res.Target.Method, res.Target.URL)
	fmt.Fprintf(w, "Key Header\n%s\n", res.Target.KeyHeader)
	for _, warn := range res.Warnings {
		fmt.Fprintf(w, "\n%s\n", warn)
	}
	fmt.Fprintf(w, "\n%s\n\n", strings.Repeat("─", ruleWidth))

	for _, c := range res.Checks {
		renderCheckLine(w, c)
	}

	for _, v := range res.Violations {
		fmt.Fprintf(w, "\n%s\n\n", strings.Repeat("─", ruleWidth))
		if v.Type == "concurrent_race" {
			fmt.Fprint(w, "RACE CONDITION DETECTED\n\n")
		} else {
			fmt.Fprint(w, "IDEMPOTENCY VIOLATION DETECTED\n\n")
		}
		renderViolationBody(w, res, v)
	}

	fmt.Fprintf(w, "\n%s\n\n", strings.Repeat("─", ruleWidth))
	fmt.Fprintf(w, "Result:\n%s\n", resultLabel(res.Summary))
}

// resultLabel is the terminal rendering of the top-level verdict. JSON keeps
// the stable string form (summary.result), including "FAILED" for exit 1.
func resultLabel(s models.Summary) string {
	switch s.ExitCode {
	case models.ExitViolation:
		return "FAIL"
	case models.ExitFailure:
		return "ERROR"
	case models.ExitInconclusive:
		return "INCONCLUSIVE"
	default:
		return "PASS"
	}
}

// renderCheckLine prints "Name ...... STATUS" with names padded to a column.
func renderCheckLine(w io.Writer, c models.Check) {
	const col = 42
	name := c.Name
	dots := col - len([]rune(name))
	if dots < 1 {
		dots = 1
	}
	fmt.Fprintf(w, "%s %s %s\n", name, strings.Repeat(".", dots), statusLabel(c.Status))
	// INCONCLUSIVE/SKIP/ERROR lines carry the reason they stopped short of
	// a verdict; FAIL details render with the violation evidence below.
	if (c.Status == models.StatusInconclusive || c.Status == models.StatusSkip ||
		c.Status == models.StatusError) && c.Detail != "" {
		fmt.Fprintf(w, "  %s\n", c.Detail)
	}
}

func statusLabel(s models.CheckStatus) string {
	switch s {
	case models.StatusPass:
		return "PASS"
	case models.StatusFail:
		return "FAIL"
	case models.StatusInconclusive:
		return "INCONCLUSIVE"
	case models.StatusSkip:
		return "SKIP"
	case models.StatusError:
		return "ERROR"
	}
	return string(s)
}

// renderViolationBody prints counts, per-fingerprint evidence, differing
// fields, and the reproducible re-run command.
func renderViolationBody(w io.Writer, res *models.Result, v models.Violation) {
	if v.Message != "" {
		fmt.Fprintf(w, "%s\n\n", v.Message)
	}

	check := findCheck(res, v.CheckID)
	groups := evidenceFor(res, v.CheckID)
	if check != nil && len(groups) > 0 {
		fmt.Fprintf(w, "Requests:\n%d\n", check.Requests)
		fmt.Fprintf(w, "\nUnique semantic responses:\n%d\n", len(groups))
		for _, g := range groups {
			fmt.Fprintf(w, "\nFingerprint %s ×%d\n", g.Label, g.Count)
			fmt.Fprintf(w, "  status: %d\n", g.Status)
			keys := make([]string, 0, len(g.Fields))
			for k := range g.Fields {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				fmt.Fprintf(w, "  %s: %v\n", k, g.Fields[k])
			}
		}
	}
	if len(v.DifferingFields) > 0 {
		fmt.Fprintln(w, "\nDiffering fields:")
		for _, f := range v.DifferingFields {
			fmt.Fprintf(w, "  %s\n", f)
		}
	}
	if check != nil && check.Detail != "" {
		fmt.Fprintf(w, "\n%s\n", check.Detail)
	}
	if res.Reproduce != nil && res.Reproduce.Command != "" {
		fmt.Fprint(w, "\nRe-run:\n\n")
		fmt.Fprintln(w, res.Reproduce.Command)
	}
}

func findCheck(res *models.Result, id string) *models.Check {
	for i := range res.Checks {
		if res.Checks[i].ID == id {
			return &res.Checks[i]
		}
	}
	return nil
}

func evidenceFor(res *models.Result, checkID string) []models.EvidenceGroup {
	var out []models.EvidenceGroup
	for _, e := range res.Evidence {
		if e.CheckID == checkID {
			out = append(out, e)
		}
	}
	return out
}
