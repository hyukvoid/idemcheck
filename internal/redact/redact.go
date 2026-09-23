// Package redact strips credential material from anything IdemCheck prints:
// terminal output, JSON reports, verbose timings, error messages, and repro
// commands.
//
// Response and request body evidence is deliberately NOT redacted — bodies
// are the evidence a verdict rests on, and hiding them would make failures
// unreadable. Do not put credentials in bodies you intend to share.
package redact

import (
	"net/url"
	"regexp"
	"strings"
	"sync"
)

// Placeholder replaces redacted material everywhere.
const Placeholder = "[REDACTED]"

// sensitiveExact holds normalized names that are always credentials.
var sensitiveExact = map[string]bool{
	"cookie":      true,
	"setcookie":   true,
	"pwd":         true,
	"auth":        true,
	"credential":  true,
	"credentials": true,
	"signature":   true,
}

// sensitiveFragments are matched as substrings of the normalized name:
// "api-key"/"api_key"/"apikey" all normalize to "apikey".
var sensitiveFragments = []string{"apikey", "token", "secret", "password", "key", "session"}

// redact extraHeaders holds per-run header names added by
// --sensitive-header / config security.sensitive_headers.
var (
	extraMu      sync.RWMutex
	extraHeaders = map[string]bool{}
)

// SetExtraHeaders registers additional header names to always redact
// (exact normalized name match). Call once before any output is produced;
// passing an empty list clears the extras.
func SetExtraHeaders(names []string) {
	extraMu.Lock()
	defer extraMu.Unlock()
	extraHeaders = map[string]bool{}
	for _, n := range names {
		if nn := normalize(n); nn != "" {
			extraHeaders[nn] = true
		}
	}
}

// IsSensitive reports whether a header or query-parameter name holds
// credentials. Matching uses the normalized name (lowercased, with "-", "_"
// and "." removed). Explicit extras registered via SetExtraHeaders always
// win. Idempotency keys are otherwise never sensitive: they are the
// reproduction signal, not a credential, and must stay visible.
func IsSensitive(name string) bool {
	n := normalize(name)
	if n == "" {
		return false
	}
	extraMu.RLock()
	extra := extraHeaders[n]
	extraMu.RUnlock()
	if extra {
		return true
	}
	if strings.HasPrefix(n, "idempotency") {
		return false
	}
	if sensitiveExact[n] {
		return true
	}
	for _, frag := range sensitiveFragments {
		if strings.Contains(n, frag) {
			return true
		}
	}
	// "authorization" (Authorization, Proxy-Authorization) and names ending
	// in "auth" (oauth, x-auth) — but not "author".
	return strings.Contains(n, "authorization") || strings.HasSuffix(n, "auth")
}

// Header returns value, or Placeholder when the header name is sensitive.
func Header(name, value string) string {
	if IsSensitive(name) {
		return Placeholder
	}
	return value
}

// HeaderFlag redacts a raw "Name: value" header flag. Non-sensitive values
// get a best-effort scrub for embedded URLs with credentials.
func HeaderFlag(raw string) string {
	i := strings.Index(raw, ":")
	if i < 0 {
		return Text(raw)
	}
	name := strings.TrimSpace(raw[:i])
	if IsSensitive(name) {
		return name + ": " + Placeholder
	}
	value := strings.TrimSpace(raw[i+1:])
	if scrubbed := Text(value); scrubbed != value {
		return name + ": " + scrubbed
	}
	return raw
}

// URL removes userinfo and redacts sensitive query-parameter values. A URL
// with nothing sensitive is returned byte-for-byte unchanged.
func URL(raw string) string {
	if raw == "" {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return Text(raw)
	}
	changed := false
	if u.User != nil {
		u.User = url.User(Placeholder)
		changed = true
	}
	q := u.Query()
	for name := range q {
		if IsSensitive(name) {
			q.Set(name, Placeholder)
			changed = true
		}
	}
	if !changed {
		return raw
	}
	u.RawQuery = q.Encode()
	return unescapePlaceholder(u.String())
}

// Text scrubs credentials from free-form text: URLs quoted inside error
// messages (userinfo and sensitive query values).
func Text(s string) string {
	if s == "" {
		return s
	}
	s = userinfoRe.ReplaceAllString(s, "://"+Placeholder+"@")
	s = queryValueRe.ReplaceAllString(s, "${1}"+Placeholder)
	return s
}

var (
	// Anything between the scheme separator and the "@" before the first
	// "/" is userinfo.
	userinfoRe = regexp.MustCompile(`://[^/@\s]+@`)
	// "?api_key=..." / "&token=..." inside a URL quoted in a message.
	queryValueRe = regexp.MustCompile(`(?i)([?&][^=&\s"]*(?:apikey|token|secret|password|key|session)[^=&\s"]*=)[^&\s"]+`)
)

// unescapePlaceholder undoes URL escaping of the placeholder so reports
// always show the literal [REDACTED].
func unescapePlaceholder(s string) string {
	return strings.ReplaceAll(s, "%5BREDACTED%5D", Placeholder)
}

func normalize(name string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		default:
			return -1
		}
	}, name)
}
