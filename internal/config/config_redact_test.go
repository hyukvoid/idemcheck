package config

import (
	"strings"
	"testing"
	"time"
)

// Malformed header errors must never echo the flag value: it may be a
// credential the user mistyped.
func TestSplitHeaderErrorHidesValue(t *testing.T) {
	_, _, err := SplitHeader("Authorization Bearer TOPSECRET")
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "TOPSECRET") {
		t.Errorf("error leaks the value: %v", err)
	}
	if !strings.Contains(err.Error(), "Authorization") {
		t.Errorf("error should still name the flag for debuggability: %v", err)
	}

	_, _, err = SplitHeader(": TOPSECRET2")
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "TOPSECRET2") {
		t.Errorf("error leaks the value: %v", err)
	}

	// A single ambiguous token (no colon at all) is hidden entirely: it may
	// be a bare credential rather than a header name.
	_, _, err = SplitHeader("sk-bare-secret")
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "sk-bare-secret") {
		t.Errorf("error leaks the value: %v", err)
	}
}

// URL validation errors must not echo credentials embedded in --url.
func TestValidateURLErrorsHideCredentials(t *testing.T) {
	o := &Options{
		URL:         "ftp://user:pass@example.test/x",
		Method:      "POST",
		Concurrency: 10,
		Repeat:      10,
		Format:      "text",
		Timeout:     time.Second,
	}
	err := o.Validate()
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "user:") || strings.Contains(err.Error(), "pass@") {
		t.Errorf("error leaks URL credentials: %v", err)
	}
}
