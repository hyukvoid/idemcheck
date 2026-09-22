package fingerprint

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// defaultIgnoredHeaders are volatile headers that change between logically
// identical responses on virtually every real deployment (LB tracing, clocks).
// Users can add more via --ignore-header / config.
var defaultIgnoredHeaders = []string{
	"date",
	"x-request-id",
	"traceparent",
	"x-amzn-trace-id",
	"cf-ray",
	"server-timing",
}

// Options controls semantic fingerprinting.
type Options struct {
	IgnoreJSONPaths []string
	IgnoreHeaders   []string // additional to the built-in defaults; lowercase
}

// Response is the raw material of a fingerprint.
type Response struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

// Fingerprint is the semantic identity of one HTTP response.
type Fingerprint struct {
	// Value is the hex SHA-256 over status + canonical body + filtered headers.
	Value string
	// Short is the Value truncated for terminal display.
	Short string
	// Status duplicates the status for convenient evidence rendering.
	Status int
	// Body is the normalized body (for structural diffing / evidence).
	Body NormalizedBody
}

// Compute derives the semantic fingerprint of a response.
func Compute(resp *Response, opts Options) (Fingerprint, error) {
	norm, err := Normalize(resp.Body, opts.IgnoreJSONPaths)
	if err != nil {
		return Fingerprint{}, err
	}

	ignored := make(map[string]bool, len(defaultIgnoredHeaders)+len(opts.IgnoreHeaders))
	for _, h := range defaultIgnoredHeaders {
		ignored[h] = true
	}
	for _, h := range opts.IgnoreHeaders {
		ignored[strings.ToLower(strings.TrimSpace(h))] = true
	}

	names := make([]string, 0, len(resp.Header))
	for name := range resp.Header {
		names = append(names, name)
	}
	sort.Strings(names)

	h := sha256.New()
	fmt.Fprintf(h, "status:%d\n", resp.StatusCode)
	fmt.Fprintf(h, "body:%s\n", norm.Canonical)
	for _, name := range names {
		lower := strings.ToLower(name)
		if ignored[lower] {
			continue
		}
		fmt.Fprintf(h, "header:%s:%s\n", lower, strings.Join(resp.Header.Values(name), ","))
	}
	sum := hex.EncodeToString(h.Sum(nil))
	return Fingerprint{
		Value:  sum,
		Short:  sum[:12],
		Status: resp.StatusCode,
		Body:   norm,
	}, nil
}
