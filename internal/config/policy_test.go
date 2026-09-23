package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolvePolicyDefaultsToSafeRetry(t *testing.T) {
	p, err := ResolvePolicy("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Profile != PolicySafeRetry {
		t.Errorf("profile = %q, want %q", p.Profile, PolicySafeRetry)
	}
	if !p.IsTransient(409) || !p.IsTransient(429) || !p.IsTransient(503) {
		t.Errorf("safe-retry must tolerate 409/429/503, got %v", p.Transient)
	}
	// Never blindly accept everything: plain 500/502/504 stay unknown.
	if p.IsTransient(500) || p.IsTransient(502) || p.IsTransient(504) {
		t.Errorf("safe-retry must not blanket-accept 5xx, got %v", p.Transient)
	}
}

func TestResolvePolicyStrictReplayAcceptsNothing(t *testing.T) {
	p, err := ResolvePolicy(PolicyStrictReplay, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range []int{409, 429, 503, 500} {
		if p.IsTransient(st) {
			t.Errorf("strict-replay must not accept %d", st)
		}
	}
}

func TestResolvePolicyOverrideAppliesToEitherProfile(t *testing.T) {
	p, err := ResolvePolicy(PolicySafeRetry, []int{409})
	if err != nil {
		t.Fatal(err)
	}
	if !p.IsTransient(409) || p.IsTransient(429) {
		t.Errorf("override must replace the profile default, got %v", p.Transient)
	}
	p, err = ResolvePolicy(PolicyStrictReplay, []int{503})
	if err != nil {
		t.Fatal(err)
	}
	if !p.IsTransient(503) || p.IsTransient(409) {
		t.Errorf("strict override must apply, got %v", p.Transient)
	}
	// An explicit empty list (non-nil) is respected, not replaced.
	p, err = ResolvePolicy(PolicySafeRetry, []int{})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Transient) != 0 {
		t.Errorf("explicit empty override must stick, got %v", p.Transient)
	}
}

func TestResolvePolicyRejectsBadInput(t *testing.T) {
	if _, err := ResolvePolicy("whatever", nil); err == nil {
		t.Error("unknown profile must be rejected")
	}
	if _, err := ResolvePolicy(PolicySafeRetry, []int{99}); err == nil {
		t.Error("status <100 must be rejected")
	}
	if _, err := ResolvePolicy(PolicySafeRetry, []int{600}); err == nil {
		t.Error("status >599 must be rejected")
	}
}

func TestWithDefaultsFillsZeroValue(t *testing.T) {
	p := Policy{}.WithDefaults()
	if p.Profile != PolicySafeRetry || !p.IsTransient(409) {
		t.Errorf("zero policy must become safe-retry, got %+v", p)
	}
	p = Policy{Profile: PolicyStrictReplay}.WithDefaults()
	if len(p.Transient) != 0 {
		t.Errorf("strict-replay default transient set must stay empty, got %v", p.Transient)
	}
	// Explicit (non-nil) lists are never overwritten.
	p = Policy{Profile: PolicySafeRetry, Transient: []int{}}.WithDefaults()
	if p.Transient == nil {
		t.Error("explicit empty list must survive WithDefaults")
	}
}

func TestLoadConfigFilePolicyAndSecurity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "idemcheck.yaml")
	content := "policy:\n  profile: strict-replay\n  transient_statuses: [409, 503]\nsecurity:\n  sensitive_headers:\n    - x-custom-secret\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	fc, err := LoadConfigFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if fc.Policy.Profile != PolicyStrictReplay {
		t.Errorf("profile = %q", fc.Policy.Profile)
	}
	if len(fc.Policy.TransientStatuses) != 2 || fc.Policy.TransientStatuses[0] != 409 {
		t.Errorf("transient_statuses = %v", fc.Policy.TransientStatuses)
	}
	if len(fc.Security.SensitiveHeaders) != 1 || fc.Security.SensitiveHeaders[0] != "x-custom-secret" {
		t.Errorf("sensitive_headers = %v", fc.Security.SensitiveHeaders)
	}
}
