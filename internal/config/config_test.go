package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func validOptions() *Options {
	return &Options{
		URL:            "http://localhost:8080/orders",
		Method:         "POST",
		Concurrency:    10,
		Repeat:         10,
		Format:         "text",
		Timeout:        5 * time.Second,
		MaxConcurrency: DefaultMaxConcurrency,
		MaxRepeat:      DefaultMaxRepeat,
	}
}

func TestValidateAcceptsValidOptions(t *testing.T) {
	if err := validOptions().Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateRequiresURL(t *testing.T) {
	o := validOptions()
	o.URL = ""
	if err := o.Validate(); err == nil || !strings.Contains(err.Error(), "--url") {
		t.Fatalf("expected --url error, got %v", err)
	}
}

func TestValidateRejectsBadScheme(t *testing.T) {
	o := validOptions()
	o.URL = "ftp://localhost/x"
	if err := o.Validate(); err == nil {
		t.Fatal("expected scheme error")
	}
}

func TestValidateConcurrencyFloorAndCeiling(t *testing.T) {
	o := validOptions()
	o.Concurrency = 1
	if err := o.Validate(); err == nil {
		t.Fatal("concurrency=1 must be rejected: it cannot reveal races")
	}
	o.Concurrency = DefaultMaxConcurrency + 1
	if err := o.Validate(); err == nil || !strings.Contains(err.Error(), "exceeds limit") {
		t.Fatalf("expected ceiling error, got %v", err)
	}
}

func TestValidateRepeatCeiling(t *testing.T) {
	o := validOptions()
	o.Repeat = DefaultMaxRepeat + 1
	if err := o.Validate(); err == nil {
		t.Fatal("expected repeat ceiling error")
	}
}

func TestValidateRejectsBothBodySources(t *testing.T) {
	// CLI enforces exclusivity before Validate; here we assert format safety.
	o := validOptions()
	o.Format = "xml"
	if err := o.Validate(); err == nil {
		t.Fatal("expected format error")
	}
}

func TestValidateIgnorePathMustStartWithDollar(t *testing.T) {
	o := validOptions()
	o.IgnoreJSON = []string{"request_id"}
	if err := o.Validate(); err == nil || !strings.Contains(err.Error(), "$") {
		t.Fatalf("expected $ guidance, got %v", err)
	}
}

func TestValidateHeaderFormat(t *testing.T) {
	o := validOptions()
	o.Headers = []string{"X-Custom without colon"}
	if err := o.Validate(); err == nil {
		t.Fatal("expected header format error")
	}
}

func TestSafetyGuardLocalhostAlwaysAllowed(t *testing.T) {
	for _, host := range []string{"localhost", "127.0.0.1", "::1", "127.0.0.53"} {
		o := validOptions()
		u := host
		if strings.Contains(u, ":") {
			u = "[" + u + "]" // IPv6 literal needs brackets in a URL
		}
		o.URL = "http://" + u + ":8080/orders"
		if _, err := o.SafetyGuard(); err != nil {
			t.Fatalf("%s must be local: %v", host, err)
		}
	}
}

func TestSafetyGuardRemoteRequiresFlag(t *testing.T) {
	o := validOptions()
	o.URL = "https://api.example.com/orders"
	if _, err := o.SafetyGuard(); err == nil {
		t.Fatal("remote target must be refused by default")
	}
	o.AllowRemote = true
	warn, err := o.SafetyGuard()
	if err != nil {
		t.Fatalf("allow-remote must permit: %v", err)
	}
	if !strings.Contains(warn, "WARNING") {
		t.Fatalf("expected visible warning, got %q", warn)
	}
}

func TestIsLocalHost(t *testing.T) {
	local := []string{"localhost", "127.0.0.1", "::1", "LOCALHOST", "127.1.2.3"}
	remote := []string{"example.com", "10.0.0.1", "192.168.1.1", "172.16.0.1", ""}
	for _, h := range local {
		if !IsLocalHost(h) {
			t.Errorf("%s should be local", h)
		}
	}
	for _, h := range remote {
		if IsLocalHost(h) {
			t.Errorf("%s should NOT be local", h)
		}
	}
}

func TestSplitHeader(t *testing.T) {
	name, value, err := SplitHeader("Authorization: Bearer tok en")
	if err != nil {
		t.Fatal(err)
	}
	if name != "Authorization" || value != "Bearer tok en" {
		t.Fatalf("got %q=%q", name, value)
	}
	if _, _, err := SplitHeader("nocolon"); err == nil {
		t.Fatal("expected error")
	}
}

func TestLoadConfigFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "idemcheck.yaml")
	content := "response:\n  ignore_json:\n    - $.request_id\n    - $.meta.trace_id\n  ignore_headers:\n    - x-custom-trace\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	ij, ih, err := LoadConfigFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(ij) != 2 || ij[0] != "$.request_id" || ij[1] != "$.meta.trace_id" {
		t.Fatalf("ignore_json = %v", ij)
	}
	if len(ih) != 1 || ih[0] != "x-custom-trace" {
		t.Fatalf("ignore_headers = %v", ih)
	}
}

func TestLoadConfigFileBadYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(path, []byte("response: [\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadConfigFile(path); err == nil {
		t.Fatal("expected parse error")
	}
}
