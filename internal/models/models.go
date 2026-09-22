// Package models defines the stable result contract of IdemCheck.
// The JSON shapes here are part of the public interface (--format json)
// and must stay backward compatible within v0.x.
package models

// Tool identity.
const (
	ToolName = "IdemCheck"
	Version  = "0.1.0"
)

// CheckStatus is the verdict of a single check.
type CheckStatus string

const (
	StatusPass  CheckStatus = "pass"
	StatusFail  CheckStatus = "fail"
	StatusWarn  CheckStatus = "warn"
	StatusSkip  CheckStatus = "skipped"
	StatusError CheckStatus = "error"
)

// Exit codes. GitHub Actions integration depends on these.
const (
	ExitPass      = 0 // all checks passed (warnings allowed)
	ExitViolation = 1 // idempotency violation detected
	ExitFailure   = 2 // invalid configuration or execution failure
)

// Target identifies the endpoint under test.
type Target struct {
	URL       string `json:"url"`
	Method    string `json:"method"`
	KeyHeader string `json:"key_header"`
	Key       string `json:"key"`
}

// Summary is the top-level verdict.
type Summary struct {
	Result        string `json:"result"` // PASS | FAILED | ERROR
	ExitCode      int    `json:"exit_code"`
	ChecksPassed  int    `json:"checks_passed"`
	ChecksFailed  int    `json:"checks_failed"`
	ChecksWarned  int    `json:"checks_warned"`
	ChecksSkipped int    `json:"checks_skipped"`
}

// RequestTiming records one request's position in time relative to the
// concurrency barrier release. Used to prove requests were simultaneous.
type RequestTiming struct {
	Worker      int   `json:"worker"`
	StartOffset int64 `json:"start_offset_ms"` // ms after barrier release
	Latency     int64 `json:"latency_ms"`
}

// Check is one executed scenario.
type Check struct {
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	Status          CheckStatus     `json:"status"`
	Requests        int             `json:"requests"`
	UniqueResponses int             `json:"unique_responses"`
	Detail          string          `json:"detail,omitempty"`
	DurationMS      int64           `json:"duration_ms"`
	Timings         []RequestTiming `json:"timings,omitempty"`
}

// Violation records a detected idempotency problem.
type Violation struct {
	CheckID         string   `json:"check"`
	Type            string   `json:"type"` // sequential_mismatch | concurrent_race
	Message         string   `json:"message"`
	DifferingFields []string `json:"differing_fields,omitempty"`
}

// EvidenceGroup is one observed semantic response and how often it occurred.
type EvidenceGroup struct {
	CheckID     string         `json:"check"`
	Label       string         `json:"label"` // A, B, C, ...
	Fingerprint string         `json:"fingerprint"`
	Count       int            `json:"count"`
	Status      int            `json:"status"`
	Fields      map[string]any `json:"fields,omitempty"` // JSON path -> value
}

// Reproduce is a copy-pasteable command that reruns the failing test.
type Reproduce struct {
	Command string `json:"command"`
}

// Result is the stable output model for --format json.
type Result struct {
	Tool       string          `json:"tool"`
	Version    string          `json:"version"`
	Target     Target          `json:"target"`
	Summary    Summary         `json:"summary"`
	Checks     []Check         `json:"checks"`
	Violations []Violation     `json:"violations"`
	Evidence   []EvidenceGroup `json:"evidence"`
	Reproduce  *Reproduce      `json:"reproduce,omitempty"`
	Warnings   []string        `json:"warnings,omitempty"`
}
