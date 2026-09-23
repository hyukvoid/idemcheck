package report

import (
	"fmt"
	"strings"

	"github.com/hyukvoid/idemcheck/internal/buildinfo"
	"github.com/hyukvoid/idemcheck/internal/config"
	"github.com/hyukvoid/idemcheck/internal/engine"
	"github.com/hyukvoid/idemcheck/internal/models"
	"github.com/hyukvoid/idemcheck/internal/redact"
)

// AssembleInput carries everything needed to build the final Result.
type AssembleInput struct {
	Target   models.Target
	Results  []engine.CheckResult
	Options  config.Options
	Warnings []string
}

// Assemble converts engine results into the stable result model: assigns
// fingerprint labels, derives violations, evidence, the repro command, and
// the summary/exit code. It is the single source of truth shared by both
// the terminal and JSON reporters.
func Assemble(in AssembleInput) *models.Result {
	res := &models.Result{
		Tool:     models.ToolName,
		Version:  buildinfo.Version(),
		Target:   in.Target,
		Warnings: in.Warnings,
	}
	// Terminal and JSON render this verbatim: scrub credentials first.
	res.Target.URL = redact.URL(res.Target.URL)

	for _, r := range in.Results {
		res.Checks = append(res.Checks, r.ToModel())
		labels := labelGroups(r)
		res.Evidence = append(res.Evidence, r.Evidence(labels)...)

		switch r.Status {
		case models.StatusPass:
			res.Summary.ChecksPassed++
		case models.StatusFail:
			res.Summary.ChecksFailed++
			res.Violations = append(res.Violations, violationFor(r))
		case models.StatusWarn:
			res.Summary.ChecksWarned++
		case models.StatusSkip:
			res.Summary.ChecksSkipped++
		}
	}

	// An execution error outranks a pass but does not erase observed
	// violations: if we already proved a race, exit 1 is more useful.
	switch {
	case hasStatus(res, models.StatusFail):
		res.Summary.Result, res.Summary.ExitCode = "FAILED", models.ExitViolation
	case hasStatus(res, models.StatusError):
		res.Summary.Result, res.Summary.ExitCode = "ERROR", models.ExitFailure
	default:
		res.Summary.Result, res.Summary.ExitCode = "PASS", models.ExitPass
	}

	if res.Summary.ExitCode == models.ExitViolation {
		res.Reproduce = &models.Reproduce{Command: ReproCommand(in.Options)}
	}
	return res
}

// labelGroups assigns display labels A, B, C... in group order (the engine
// already sorts groups by descending count, matching the README examples).
func labelGroups(r engine.CheckResult) map[string]string {
	labels := map[string]string{}
	for i, g := range r.Groups {
		labels[g.Fingerprint.Value] = string(rune('A' + i))
	}
	return labels
}

func violationFor(r engine.CheckResult) models.Violation {
	v := models.Violation{
		CheckID:         r.ID,
		DifferingFields: r.DifferingFields,
	}
	if r.ID == engine.ScConcurrent {
		v.Type = "concurrent_race"
		v.Message = fmt.Sprintf("%d concurrent requests\n1 idempotency key\n%d unique semantic responses",
			r.Requests, r.Unique)
	} else {
		v.Type = "sequential_mismatch"
		v.Message = fmt.Sprintf("%d sequential requests with the same key produced %d unique semantic responses",
			r.Requests, r.Unique)
	}
	return v
}

func hasStatus(res *models.Result, s models.CheckStatus) bool {
	for _, c := range res.Checks {
		if c.Status == s {
			return true
		}
	}
	return false
}

// ReproCommand renders a copy-pasteable command that reproduces the run
// with the exact deterministic key.
func ReproCommand(o config.Options) string {
	var b strings.Builder
	b.WriteString("idemcheck test")
	fmt.Fprintf(&b, " \\\n  --url %s", shellQuote(redact.URL(o.URL)))
	if o.Method != config.DefaultMethod {
		fmt.Fprintf(&b, " \\\n  --method %s", shellQuote(o.Method))
	}
	for _, h := range o.Headers {
		fmt.Fprintf(&b, " \\\n  -H %s", shellQuote(redact.HeaderFlag(h)))
	}
	if o.BodyIsFile {
		fmt.Fprintf(&b, " \\\n  --body-file %s", shellQuote(o.BodySource))
	} else if o.BodySource != "" {
		fmt.Fprintf(&b, " \\\n  --body %s", shellQuote(o.BodySource))
	}
	if o.Concurrency != config.DefaultConcurrency {
		fmt.Fprintf(&b, " \\\n  --concurrency %d", o.Concurrency)
	}
	if o.Repeat != config.DefaultRepeat {
		fmt.Fprintf(&b, " \\\n  --repeat %d", o.Repeat)
	}
	fmt.Fprintf(&b, " \\\n  --key %s", shellQuote(o.Key))
	if o.AllowRemote {
		b.WriteString(" \\\n  --allow-remote")
	}
	return b.String()
}

func shellQuote(s string) string {
	if s == "" {
		return `""`
	}
	if strings.ContainsAny(s, " \t\"'$\\") {
		return `'` + strings.ReplaceAll(s, `'`, `'\''`) + `'`
	}
	return s
}
