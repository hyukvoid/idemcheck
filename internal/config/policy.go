package config

import "fmt"

// Verdict policy profiles. The policy decides which non-2xx statuses are
// "acceptable transients" (tolerated retry outcomes) and which are unknown
// (cannot be classified -> INCONCLUSIVE). It never turns uncertainty into a
// pass: FAIL still requires divergent response bodies.
const (
	// PolicySafeRetry tolerates common retry outcomes: 409 (conflict),
	// 429 (rate limit), 503 (unavailable) — when exactly one logical
	// result was observed.
	PolicySafeRetry = "safe-retry"
	// PolicyStrictReplay accepts no transient statuses at all: any non-2xx
	// response makes the observation inconclusive.
	PolicyStrictReplay = "strict-replay"

	// DefaultPolicyProfile is used when neither flag nor config sets one.
	DefaultPolicyProfile = PolicySafeRetry
)

// Policy is the resolved verdict policy: profile name plus the concrete
// transient status set (after overrides).
type Policy struct {
	Profile   string
	Transient []int
}

// DefaultTransient returns a profile's default transient statuses.
// strict-replay accepts none.
func DefaultTransient(profile string) []int {
	if profile == PolicyStrictReplay {
		return nil
	}
	return []int{409, 429, 503}
}

// DefaultPolicy is safe-retry with its default transient set.
func DefaultPolicy() Policy {
	return Policy{Profile: PolicySafeRetry, Transient: DefaultTransient(PolicySafeRetry)}
}

// ResolvePolicy merges a profile name with explicit status overrides.
// An empty profile selects the default; a non-nil override replaces the
// profile's default list for either profile.
func ResolvePolicy(profile string, overrides []int) (Policy, error) {
	if profile == "" {
		profile = DefaultPolicyProfile
	}
	if profile != PolicySafeRetry && profile != PolicyStrictReplay {
		return Policy{}, fmt.Errorf("--policy must be %q or %q (got %q)",
			PolicySafeRetry, PolicyStrictReplay, profile)
	}
	transient := overrides
	if transient == nil {
		transient = DefaultTransient(profile)
	}
	for _, s := range transient {
		if s < 100 || s > 599 {
			return Policy{}, fmt.Errorf("transient status %d out of range: must be a valid HTTP status code (100-599)", s)
		}
	}
	return Policy{Profile: profile, Transient: transient}, nil
}

// WithDefaults fills unspecified fields with profile defaults so a partially
// constructed Policy (zero value in tests/integration) behaves predictably.
func (p Policy) WithDefaults() Policy {
	if p.Profile == "" {
		return DefaultPolicy()
	}
	if p.Transient == nil {
		p.Transient = DefaultTransient(p.Profile)
	}
	return p
}

// IsTransient reports whether status is an acceptable transient under this
// policy. Everything else non-2xx is unknown: it can only yield FAIL (when
// divergent success bodies exist alongside it) or INCONCLUSIVE.
func (p Policy) IsTransient(status int) bool {
	for _, s := range p.Transient {
		if s == status {
			return true
		}
	}
	return false
}
