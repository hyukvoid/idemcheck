package config

import (
	"strings"
	"testing"
	"time"
)

// Unset trials options normalize to the conservative defaults; explicit
// values inside the ceiling are kept.
func TestValidateTrialsDefaults(t *testing.T) {
	o := validOptions() // Trials/MaxTrials/Settle/ReplayTimeout all zero
	if err := o.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if o.Trials != DefaultTrials {
		t.Errorf("trials = %d, want default %d", o.Trials, DefaultTrials)
	}
	if o.MaxTrials != DefaultMaxTrials {
		t.Errorf("max trials = %d, want default %d", o.MaxTrials, DefaultMaxTrials)
	}
	if o.Settle != DefaultSettle {
		t.Errorf("settle = %s, want %s", o.Settle, DefaultSettle)
	}
	if o.ReplayTimeout != DefaultReplayTimeout {
		t.Errorf("replay timeout = %s, want %s", o.ReplayTimeout, DefaultReplayTimeout)
	}
}

func TestValidateTrialsExplicitValueKept(t *testing.T) {
	o := validOptions()
	o.Trials, o.MaxTrials = 5, 10
	if err := o.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if o.Trials != 5 {
		t.Errorf("trials = %d, want 5 (explicit value must be kept)", o.Trials)
	}
}

// Raising trials past the ceiling requires raising --max-trials too: a
// remote target must not be hammered by a fat finger.
func TestValidateTrialsCeiling(t *testing.T) {
	o := validOptions()
	o.Trials = DefaultMaxTrials + 1
	if err := o.Validate(); err == nil || !strings.Contains(err.Error(), "exceeds limit") {
		t.Fatalf("expected ceiling error, got %v", err)
	}
	o.MaxTrials = DefaultMaxTrials + 5
	if err := o.Validate(); err != nil {
		t.Errorf("raising --max-trials must permit it: %v", err)
	}
}

func TestValidateRejectsNegativeTrialOptions(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Options)
		want   string
	}{
		{"negative trials", func(o *Options) { o.Trials = -1 }, "--trials"},
		{"negative settle", func(o *Options) { o.Settle = -time.Second }, "--settle"},
		{"negative replay timeout", func(o *Options) { o.ReplayTimeout = -time.Second }, "--replay-timeout"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o := validOptions()
			c.mutate(o)
			if err := o.Validate(); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("expected %s error, got %v", c.want, err)
			}
		})
	}
}
