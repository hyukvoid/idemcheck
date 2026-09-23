package report

import (
	"strings"
	"testing"

	"github.com/hyukvoid/idemcheck/internal/config"
	"github.com/hyukvoid/idemcheck/internal/models"
)

// The repro command is meant to be pasted into issue trackers: it must carry
// no credentials while staying copy-pasteable for everything else.
func TestReproCommandRedactsCredentials(t *testing.T) {
	o := config.Options{
		URL:    "https://user:pass@example.test/orders?api_key=sekrit&page=2",
		Method: "POST",
		Headers: []string{
			"Authorization: Bearer supersecret",
			"X-Trace: abc",
		},
		Key: "repro-key-1",
	}
	cmd := ReproCommand(o)

	for _, leak := range []string{"user:pass", "sekrit", "supersecret"} {
		if strings.Contains(cmd, leak) {
			t.Errorf("repro command leaks %q:\n%s", leak, cmd)
		}
	}
	for _, want := range []string{
		"api_key=" + "[REDACTED]",
		"Authorization: [REDACTED]",
		"X-Trace: abc", // non-sensitive headers survive
		"--key repro-key-1",
		"page=2",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("repro command must keep %q:\n%s", want, cmd)
		}
	}
}

// The assembled target is rendered verbatim by terminal and JSON output.
func TestAssembleRedactsTargetURL(t *testing.T) {
	res := Assemble(AssembleInput{
		Target:  models.Target{URL: "https://user:pass@example.test/x?token=sekrit"},
		Options: config.Options{},
	})
	if strings.Contains(res.Target.URL, "user:pass") || strings.Contains(res.Target.URL, "token=sekrit") {
		t.Errorf("target URL leaks credentials: %s", res.Target.URL)
	}
}
