package redact

import "testing"

func TestIsSensitiveHeaders(t *testing.T) {
	sensitive := []string{
		"Authorization", "authorization", "PROXY-AUTHORIZATION",
		"Cookie", "cookie", "Set-Cookie",
		"X-Api-Key", "api-key", "api_key", "API_KEY",
		"X-Auth-Token", "X-ApiToken", "Key",
		"X-Password", "X-Amz-Security-Token", "X-Session-Id",
		"oauth", "X-OAuth-Authorization",
	}
	for _, h := range sensitive {
		if !IsSensitive(h) {
			t.Errorf("%q must be treated as sensitive", h)
		}
	}

	notSensitive := []string{
		// The idempotency key is the reproduction signal, never a credential.
		"Idempotency-Key", "Idempotency-Token", "idempotency_key",
		"Content-Type", "Accept-Encoding", "User-Agent",
		"X-Request-Id", "Traceparent", "X-Amzn-Trace-Id",
		"Author", // ends near "auth" but is not a credential
	}
	for _, h := range notSensitive {
		if IsSensitive(h) {
			t.Errorf("%q must NOT be treated as sensitive", h)
		}
	}
}

func TestHeaderValues(t *testing.T) {
	if got := Header("Authorization", "Bearer sekrit"); got != Placeholder {
		t.Errorf("sensitive header value must be %s, got %q", Placeholder, got)
	}
	if got := Header("X-Trace", "abc"); got != "abc" {
		t.Errorf("plain header must survive, got %q", got)
	}
}

func TestHeaderFlag(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Authorization: Bearer sekrit", "Authorization: " + Placeholder},
		{"X-Api-Key: sekrit", "X-Api-Key: " + Placeholder},
		{"X-Trace: abc", "X-Trace: abc"},
		// Non-sensitive values still get URLs scrubbed.
		{"X-Callback: https://user:pass@hook.test/x", "X-Callback: https://" + Placeholder + "@hook.test/x"},
	}
	for _, c := range cases {
		if got := HeaderFlag(c.in); got != c.want {
			t.Errorf("HeaderFlag(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://user:pass@example.test/api", "https://" + Placeholder + "@example.test/api"},
		{"http://user@example.test/", "http://" + Placeholder + "@example.test/"},
		{"https://example.test/api?api_key=sekrit&page=2", "https://example.test/api?api_key=" + Placeholder + "&page=2"},
		{"http://example.test/x?key=abc", "http://example.test/x?key=" + Placeholder},
		// Idempotency keys stay visible: they are needed to reproduce.
		{"http://example.test/x?idempotency_key=ord-1", "http://example.test/x?idempotency_key=ord-1"},
		// Nothing sensitive: byte-for-byte unchanged, no round-trip drift.
		{"http://example.test/x?page=2&sort=asc", "http://example.test/x?page=2&sort=asc"},
		// Not a URL at all: unchanged.
		{"", ""},
	}
	for _, c := range cases {
		if got := URL(c.in); got != c.want {
			t.Errorf("URL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestURLIdempotentAndUnescaped(t *testing.T) {
	in := "https://user:pass@example.test/api?api_key=sekrit"
	once := URL(in)
	if twice := URL(once); twice != once {
		t.Errorf("redaction must be idempotent: %q -> %q", once, twice)
	}
	if out := URL(in); containsPlaceholderEscaped(out) {
		t.Errorf("placeholder must stay literal: %q", out)
	}
}

func containsPlaceholderEscaped(s string) bool {
	return len(s) > 0 && (contains(s, "%5BREDACTED%5D") || contains(s, "%5bredacted%5d"))
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestText(t *testing.T) {
	cases := []struct{ in, want string }{
		{`Get "https://user:pass@example.test/x": dial tcp: boom`,
			`Get "https://` + Placeholder + `@example.test/x": dial tcp: boom`},
		{`failed https://example.test/x?api_key=sekrit&y=2 after 3s`,
			`failed https://example.test/x?api_key=` + Placeholder + `&y=2 after 3s`},
		{`plain message without urls`, `plain message without urls`},
		// No userinfo-shaped false positive on paths.
		{`https://example.test/@mention`, `https://example.test/@mention`},
	}
	for _, c := range cases {
		if got := Text(c.in); got != c.want {
			t.Errorf("Text(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
