package config

import (
	"strings"
	"testing"

	"github.com/hyukvoid/idemcheck/internal/httpx"
)

// Fault injection is a fixed vocabulary: unknown values are typos.
func TestValidateFaultMode(t *testing.T) {
	for _, mode := range []string{"", httpx.FaultNone, httpx.FaultLostResponse} {
		o := validOptions()
		o.Fault = mode
		if err := o.Validate(); err != nil {
			t.Errorf("fault %q must be accepted: %v", mode, err)
		}
	}
	o := validOptions()
	o.Fault = "meltdown"
	if err := o.Validate(); err == nil || !strings.Contains(err.Error(), "--fault") {
		t.Fatalf("expected --fault error, got %v", err)
	}
}

// The empty value normalizes so callers never branch on two spellings of
// "no fault".
func TestValidateFaultDefaultsToNone(t *testing.T) {
	o := validOptions()
	if err := o.Validate(); err != nil {
		t.Fatal(err)
	}
	if o.Fault != httpx.FaultNone {
		t.Errorf("fault = %q, want %q", o.Fault, httpx.FaultNone)
	}
}
