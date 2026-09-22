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

	MaxConcurrency int
	MaxRepeat      int
}

// fileConfig mirrors the YAML config file.
type fileConfig struct {
	Response struct {
		IgnoreJSON    []string `yaml:"ignore_json"`
		IgnoreHeaders []string `yaml:"ignore_headers"`
	} `yaml:"response"`
}

// LoadConfigFile reads an optional YAML config and returns its ignore lists.
func LoadConfigFile(path string) (ignoreJSON, ignoreHeaders []string, err error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("read config %s: %w", path, err)
	}
	var fc fileConfig
	if err := yaml.Unmarshal(raw, &fc); err != nil {
		return nil, nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	return fc.Response.IgnoreJSON, fc.Response.IgnoreHeaders, nil
}

// Validate checks options for internal consistency. Errors map to exit code 2.
func (o *Options) Validate() error {
	if strings.TrimSpace(o.URL) == "" {
		return fmt.Errorf("--url is required")
	}
	u, err := url.Parse(o.URL)
	if err != nil {
		return fmt.Errorf("invalid --url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("invalid --url %q: scheme must be http or https", o.URL)
	}
	if u.Host == "" {
		return fmt.Errorf("invalid --url %q: missing host", o.URL)
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

	switch o.Format {
	case "text", "json":
	default:
		return fmt.Errorf("--format must be text or json (got %q)", o.Format)
	}

	if o.Timeout <= 0 {
		return fmt.Errorf("--timeout must be positive")
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
		return "", fmt.Errorf("invalid --url: %w", err)
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

// SplitHeader parses a "Name: value" header flag.
func SplitHeader(h string) (name, value string, err error) {
	i := strings.Index(h, ":")
	if i < 0 {
		return "", "", fmt.Errorf("invalid header %q: expected \"Name: value\"", h)
	}
	name = strings.TrimSpace(h[:i])
	value = strings.TrimSpace(h[i+1:])
	if name == "" {
		return "", "", fmt.Errorf("invalid header %q: empty name", h)
	}
	return name, value, nil
}
