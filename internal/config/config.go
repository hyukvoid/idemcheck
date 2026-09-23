// Package config holds CLI options, validation, the YAML config file, and
// the safety guard that keeps IdemCheck away from production by default.
package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/hyukvoid/idemcheck/internal/httpx"
	"github.com/hyukvoid/idemcheck/internal/redact"
	"gopkg.in/yaml.v3"
)

// Conservative default limits. Sending hundreds of duplicate POSTs is the
// whole point of this tool, so the ceiling is deliberately low.
const (
	DefaultConcurrency = 10
	DefaultRepeat      = 10
	DefaultKeyHeader   = "Idempotency-Key"
	DefaultMethod      = "POST"
	DefaultFormat      = "text"
	DefaultTimeout     = 10 * time.Second

	// Trials is how many isolated concurrent bursts run by default;
	// MaxTrials is the deliberate ceiling for raising it.
	DefaultTrials    = 1
	DefaultMaxTrials = 10

	// Settle waits after the burst so in-flight server work finishes
	// before the replay; ReplayTimeout bounds the replay retry budget.
	DefaultSettle        = 250 * time.Millisecond
	DefaultReplayTimeout = 5 * time.Second

	// Hard ceilings unless explicitly raised.
	DefaultMaxConcurrency = 50
	DefaultMaxRepeat      = 100
)

// Options is the fully resolved configuration of one `idemcheck test` run.
type Options struct {
	URL         string
	Method      string
	Body        []byte
	BodySource  string // original --body value or file path (for repro)
	BodyIsFile  bool
	Headers     []string // raw "K: V" pairs from -H, order preserved
	KeyHeader   string
	Key         string // base idempotency key; generated when empty
	Concurrency int
	Repeat      int
	Format      string
	AllowRemote bool
	Timeout     time.Duration
	Verbose     bool

	IgnoreJSON   []string
	IgnoreHeader []string

	// Trials is how many isolated concurrent bursts the concurrency check
	// runs; MaxTrials is the ceiling a user must raise deliberately.
	Trials    int
	MaxTrials int
	// Settle waits after each burst before the replay; ReplayTimeout
	// bounds that replay's retry budget.
	Settle        time.Duration
	ReplayTimeout time.Duration

	// Policy selects verdict semantics: safe-retry (default) or
	// strict-replay. TransientStatuses overrides the profile's default
	// transient set when non-nil.
	Policy            string
	TransientStatuses []int
	// Fault selects deterministic fault injection by a local reverse proxy
	// in front of the target (none | lost-response). It never changes the
	// request, only what the local hop does with the answer.
	Fault string
	// SensitiveHeaders are extra header names to redact from all output.
	SensitiveHeaders []string
	// MaxBodyBytes bounds response reads; 0 means the httpx default
	// (4 MiB). Negative values are rejected.
	MaxBodyBytes int64

	MaxConcurrency int
	MaxRepeat      int
}

// FileConfig is the parsed YAML configuration file.
type FileConfig struct {
	Response struct {
		IgnoreJSON    []string `yaml:"ignore_json"`
		IgnoreHeaders []string `yaml:"ignore_headers"`
	} `yaml:"response"`
	Policy struct {
		Profile           string `yaml:"profile"`
		TransientStatuses []int  `yaml:"transient_statuses"`
	} `yaml:"policy"`
	Security struct {
		SensitiveHeaders []string `yaml:"sensitive_headers"`
	} `yaml:"security"`
}

// LoadConfigFile reads an optional YAML config file.
func LoadConfigFile(path string) (FileConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return FileConfig{}, fmt.Errorf("read config %s: %w", path, err)
	}
	var fc FileConfig
	if err := yaml.Unmarshal(raw, &fc); err != nil {
		return FileConfig{}, fmt.Errorf("parse config %s: %w", path, err)
	}
	return fc, nil
}

// Validate checks options for internal consistency. Errors map to exit code 2.
func (o *Options) Validate() error {
	if strings.TrimSpace(o.URL) == "" {
		return fmt.Errorf("--url is required")
	}
	u, err := url.Parse(o.URL)
	if err != nil {
		return fmt.Errorf("invalid --url: %s", redact.Text(err.Error()))
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("invalid --url %q: scheme must be http or https", redact.URL(o.URL))
	}
	if u.Host == "" {
		return fmt.Errorf("invalid --url %q: missing host", redact.URL(o.URL))
	}

	o.Method = strings.ToUpper(strings.TrimSpace(o.Method))
	if o.Method == "" {
		return fmt.Errorf("--method must not be empty")
	}

	if o.Concurrency < 2 {
		return fmt.Errorf("--concurrency must be at least 2 to test for races (got %d)", o.Concurrency)
	}
	if o.Concurrency > o.MaxConcurrency {
		return fmt.Errorf("--concurrency %d exceeds limit %d (raise --max-concurrency deliberately if you really want this)", o.Concurrency, o.MaxConcurrency)
	}
	if o.Repeat < 2 {
		return fmt.Errorf("--repeat must be at least 2 (got %d)", o.Repeat)
	}
	if o.Repeat > o.MaxRepeat {
		return fmt.Errorf("--repeat %d exceeds limit %d (raise --max-repeat deliberately if you really want this)", o.Repeat, o.MaxRepeat)
	}

	// Trials: 0 means "use the default"; negatives are user error, and
	// exceeding the ceiling requires raising --max-trials on purpose.
	if o.Trials < 0 {
		return fmt.Errorf("--trials must not be negative (got %d)", o.Trials)
	}
	if o.Trials == 0 {
		o.Trials = DefaultTrials
	}
	if o.MaxTrials <= 0 {
		o.MaxTrials = DefaultMaxTrials
	}
	if o.Trials > o.MaxTrials {
		return fmt.Errorf("--trials %d exceeds limit %d (raise --max-trials deliberately if you really want this)", o.Trials, o.MaxTrials)
	}
	if o.Settle < 0 {
		return fmt.Errorf("--settle must not be negative (got %s)", o.Settle)
	}
	if o.Settle == 0 {
		o.Settle = DefaultSettle
	}
	if o.ReplayTimeout < 0 {
		return fmt.Errorf("--replay-timeout must not be negative (got %s)", o.ReplayTimeout)
	}
	if o.ReplayTimeout == 0 {
		o.ReplayTimeout = DefaultReplayTimeout
	}

	// Fault modes are a fixed vocabulary: unknown values mean a typo, not
	// an invitation to guess.
	if o.Fault == "" {
		o.Fault = httpx.FaultNone
	}
	if !httpx.ValidFault(o.Fault) {
		return fmt.Errorf("--fault must be %q or %q (got %q)",
			httpx.FaultNone, httpx.FaultLostResponse, o.Fault)
	}

	switch o.Format {
	case "text", "json":
	default:
		return fmt.Errorf("--format must be text or json (got %q)", o.Format)
	}

	if o.Timeout <= 0 {
		return fmt.Errorf("--timeout must be positive")
	}

	if _, err := ResolvePolicy(o.Policy, o.TransientStatuses); err != nil {
		return err
	}
	if o.MaxBodyBytes < 0 {
		return fmt.Errorf("--max-body-bytes must not be negative (got %d)", o.MaxBodyBytes)
	}

	for _, h := range o.Headers {
		if _, _, err := SplitHeader(h); err != nil {
			return err
		}
	}
	for _, p := range o.IgnoreJSON {
		if !strings.HasPrefix(p, "$") {
			return fmt.Errorf("invalid --ignore-json path %q: must start with $ (example: $.request_id)", p)
		}
	}
	return nil
}

// SafetyGuard refuses remote targets unless --allow-remote is set.
// Returns a warning to display (possibly empty).
func (o *Options) SafetyGuard() (string, error) {
	u, err := url.Parse(o.URL)
	if err != nil {
		return "", fmt.Errorf("invalid --url: %s", redact.Text(err.Error()))
	}
	host := u.Hostname()
	if IsLocalHost(host) {
		return "", nil
	}
	if !o.AllowRemote {
		return "", fmt.Errorf("refusing to test non-local target %q: this tool intentionally sends duplicate POST requests.\n"+
			"Re-run with --allow-remote if %s is a test/staging environment you own", host, host)
	}
	return fmt.Sprintf("WARNING: --allow-remote set; hammering %s with duplicate requests. Make sure it is not production.", host), nil
}

// IsLocalHost reports whether host is a loopback name or address.
func IsLocalHost(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// SplitHeader parses a "Name: value" header flag. Errors never echo the
// value: the flag may be a credential the user mistyped.
func SplitHeader(h string) (name, value string, err error) {
	i := strings.Index(h, ":")
	if i < 0 {
		// Show only the leading token (the attempted name); anything after
		// it may be a credential.
		if j := strings.IndexAny(h, " \t"); j >= 0 {
			return "", "", fmt.Errorf("invalid header %q: expected \"Name: value\"",
				h[:j]+" "+redact.Placeholder)
		}
		// One ambiguous token: it may be a bare credential rather than a
		// header name, so hide it entirely.
		return "", "", fmt.Errorf("invalid header: expected \"Name: value\" (flag hidden: no colon found)")
	}
	name = strings.TrimSpace(h[:i])
	value = strings.TrimSpace(h[i+1:])
	if name == "" {
		return "", "", fmt.Errorf("invalid header: empty name (value hidden)")
	}
	return name, value, nil
}
