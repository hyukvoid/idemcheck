package cli

import (
	"strings"
	"testing"

	"github.com/hyukvoid/idemcheck/internal/httpx"
	"github.com/hyukvoid/idemcheck/internal/redact"
)

// Regression: the FAULT warning prints the local proxy URL, which inherits
// the target's query string verbatim. Sensitive query values and URL
// userinfo must be redacted on this output path exactly like every other
// one (terminal report, JSON target, repro command, transport errors).
func TestFaultWarningRedactsCredentials(t *testing.T) {
	msg := faultWarning(httpx.FaultLostResponse,
		"http://127.0.0.1:60963/orders?api_key=SUPER_SECRET_QUERY&page=2",
		"http://alice:SUPER_SECRET_USERPASS@127.0.0.1:18081/orders?api_key=SUPER_SECRET_QUERY")

	for _, leak := range []string{"SUPER_SECRET_QUERY", "SUPER_SECRET_USERPASS"} {
		if strings.Contains(msg, leak) {
			t.Errorf("fault warning leaks %q: %s", leak, msg)
		}
	}
	// The message must stay informative: proxy address, path and the
	// redaction placeholder all remain visible.
	for _, want := range []string{redact.Placeholder, "http://127.0.0.1:60963/orders"} {
		if !strings.Contains(msg, want) {
			t.Errorf("fault warning must keep %q: %s", want, msg)
		}
	}
	// Nonsensitive query values are not credentials and stay visible.
	if !strings.Contains(msg, "page=2") {
		t.Errorf("nonsensitive query must stay visible: %s", msg)
	}
}
