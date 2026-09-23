package redact

import "testing"

// --sensitive-header registers extra names; explicit intent wins over the
// idempotency carve-out.
func TestSetExtraHeaders(t *testing.T) {
	t.Cleanup(func() { SetExtraHeaders(nil) })

	// X-Corp-Trace is not sensitive by any default rule (no token/key/
	// auth/cookie substring).
	SetExtraHeaders([]string{"X-Corp-Trace", "  ", "IDEMPOTENCY-KEY"})
	if !IsSensitive("x-corp-trace") {
		t.Error("registered header must be sensitive (normalized match)")
	}
	if !IsSensitive("Idempotency-Key") {
		t.Error("explicit registration must override the idempotency carve-out")
	}
	if got := Header("X-Corp-Trace", "sekrit"); got != Placeholder {
		t.Errorf("registered header value must be redacted, got %q", got)
	}
	if IsSensitive("X-Request-Id") {
		t.Error("unregistered header must stay visible")
	}

	// Clearing restores defaults.
	SetExtraHeaders(nil)
	if IsSensitive("x-corp-trace") {
		t.Error("extras must be clearable")
	}
	if IsSensitive("Idempotency-Key") {
		t.Error("idempotency carve-out must return after clearing")
	}
}
