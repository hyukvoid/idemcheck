package buildinfo

import (
	"strings"
	"testing"
)

// The normalization must round-trip what the go tool actually stamps:
// tagged installs, untagged dev builds, and pseudo-versions.
func TestFromModuleVersion(t *testing.T) {
	cases := map[string]string{
		"v0.1.3":                               "0.1.3",
		"(devel)":                              "devel",
		"":                                     "devel",
		"v0.1.4-0.20260922120000-abcdef123456": "0.1.4-0.20260922120000-abcdef123456",
	}
	for in, want := range cases {
		if got := fromModuleVersion(in); got != want {
			t.Errorf("fromModuleVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestToDisplay(t *testing.T) {
	cases := map[string]string{
		"0.1.3": "v0.1.3",
		"devel": "devel",
		"":      "devel",
		// Defensive: a value that already carries "v" must not gain a second.
		"v0.1.3": "v0.1.3",
	}
	for in, want := range cases {
		if got := toDisplay(in); got != want {
			t.Errorf("toDisplay(%q) = %q, want %q", in, got, want)
		}
	}
}

// Version must always be usable in output: non-empty, bare (no leading "v",
// so callers can decide the display form), and stable within one process.
func TestVersionContract(t *testing.T) {
	v := Version()
	if v == "" {
		t.Fatal("Version() must never be empty")
	}
	if strings.HasPrefix(v, "v") {
		t.Errorf("Version() = %q: must be bare; Display() adds the v prefix", v)
	}
	if again := Version(); again != v {
		t.Errorf("Version() not stable: %q then %q", v, again)
	}
}

func TestDisplayContract(t *testing.T) {
	d := Display()
	if d == "" {
		t.Fatal("Display() must never be empty")
	}
	if d != "devel" && !strings.HasPrefix(d, "v") {
		t.Errorf("Display() = %q: release versions must start with v", d)
	}
}
