package config

import (
	"regexp"
	"testing"
)

func TestNewKeyShape(t *testing.T) {
	re := regexp.MustCompile(`^idemcheck-[0-9a-f]{12}$`)
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		k := NewKey()
		if !re.MatchString(k) {
			t.Fatalf("key %q does not match idemcheck-<hex>", k)
		}
		if seen[k] {
			t.Fatalf("duplicate key generated: %s", k)
		}
		seen[k] = true
	}
}

func TestScenarioKeyIsolation(t *testing.T) {
	base := "idemcheck-abc123"
	a := ScenarioKey(base, "seq2")
	b := ScenarioKey(base, "concurrent")
	if a == b {
		t.Fatal("different scenarios must never share a key")
	}
	if a != base+"-seq2" {
		t.Fatalf("got %q", a)
	}
	// Deterministic: same inputs, same key (reproducibility contract).
	if ScenarioKey(base, "seq2") != a {
		t.Fatal("scenario key derivation must be deterministic")
	}
}
