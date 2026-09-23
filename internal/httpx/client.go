// Package httpx builds isolated HTTP requests and executes them with
// sane timeouts. Every call gets its own *http.Request so concurrent
// workers never share mutable request state.
package httpx

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/hyukvoid/idemcheck/internal/redact"
)

// DefaultMaxBodyBytes bounds how much of each response body is read. Bodies
// larger than the limit are never buffered or fingerprinted; the attempt is
// reported as oversize so the verdict stays inconclusive instead of guessing
// from a truncated body.
const DefaultMaxBodyBytes int64 = 4 << 20 // 4 MiB

// errBodyTooLarge signals a decompressed body that exceeded the read limit.
var errBodyTooLarge = errors.New("response body exceeds read limit")

// Spec describes the logical request under test. It is immutable; a fresh
// concrete request is materialized per attempt.
type Spec struct {
	Method    string
	URL       string
	Header    http.Header // base headers (auth, content-type) without the key
	Body      []byte
	KeyHeader string
}

// Outcome is one executed request with its timing.
type Outcome struct {
	Worker      int
	Resp        *http.Response
	Body        []byte
	StatusCode  int
	Header      http.Header
	Latency     time.Duration
	StartOffset time.Duration // set by the concurrency engine (after barrier)
	// Oversize is true when the (decoded) body exceeded the read limit.
	// Body is then empty and Err explains why. Callers must treat an
	// oversize attempt as insufficient evidence, never as a comparison
	// result.
	Oversize bool
	Err      error
}

// Client wraps net/http with per-request timeouts.
type Client struct {
	hc      *http.Client
	timeout time.Duration
	maxBody int64
}

// NewClient builds a client. The transport pools connections so a burst of
// concurrent workers does not serialize on TCP setup.
func NewClient(timeout time.Duration) *Client {
	transport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 100,
		IdleConnTimeout:     30 * time.Second,
	}
	return &Client{
		hc: &http.Client{
			Transport: transport,
			Timeout:   timeout,
			// Idempotency bugs often manifest as 3xx; never auto-follow.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		timeout: timeout,
		maxBody: DefaultMaxBodyBytes,
	}
}

// SetMaxBody overrides the response body read limit in bytes. Values <= 0
// restore DefaultMaxBodyBytes.
func (c *Client) SetMaxBody(n int64) {
	if n <= 0 {
		n = DefaultMaxBodyBytes
	}
	c.maxBody = n
}

// BuildRequest materializes a concrete request for one attempt.
func (s Spec) BuildRequest(ctx context.Context, key string) (*http.Request, error) {
	var body io.Reader
	if len(s.Body) > 0 {
		body = bytes.NewReader(s.Body)
	}
	req, err := http.NewRequestWithContext(ctx, s.Method, s.URL, body)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	for k, vs := range s.Header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if s.KeyHeader != "" {
		req.Header.Set(s.KeyHeader, key)
	}
	if len(s.Body) > 0 && req.Header.Get("Content-Type") == "" {
		trimmed := bytes.TrimSpace(s.Body)
		if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') {
			req.Header.Set("Content-Type", "application/json")
		}
	}
	return req, nil
}

// Do executes one request and reads the full response body (up to the
// configured limit).
func (c *Client) Do(ctx context.Context, spec Spec, key string, worker int) Outcome {
	req, err := spec.BuildRequest(ctx, key)
	if err != nil {
		return Outcome{Worker: worker, Err: err}
	}
	// Ask for an uncompressed body so fingerprints never depend on content
	// negotiation; servers that ignore this are decoded best-effort below.
	if req.Header.Get("Accept-Encoding") == "" {
		req.Header.Set("Accept-Encoding", "identity")
	}
	start := time.Now()
	resp, err := c.hc.Do(req)
	latency := time.Since(start)
	if err != nil {
		return Outcome{Worker: worker, Latency: latency, Err: err}
	}
	defer resp.Body.Close()

	header := resp.Header.Clone()
	body, oversize, err := readBounded(resp.Body, c.maxBody)
	switch {
	case err != nil:
		return Outcome{Worker: worker, Latency: latency, StatusCode: resp.StatusCode, Header: header,
			Err: fmt.Errorf("read body: %w", err)}
	case oversize:
		return Outcome{Worker: worker, Latency: latency, StatusCode: resp.StatusCode, Header: header, Oversize: true,
			Err: fmt.Errorf("response body exceeds %d byte limit", c.maxBody)}
	}
	if decoded, handled, decOversize := decodeBody(header, body, c.maxBody); handled {
		if decOversize {
			return Outcome{Worker: worker, Latency: latency, StatusCode: resp.StatusCode, Header: header, Oversize: true,
				Err: fmt.Errorf("decoded response body exceeds %d byte limit", c.maxBody)}
		}
		body = decoded
		// The stored bytes are plain now; stale encodings would corrupt
		// evidence.
		header.Del("Content-Encoding")
		header.Del("Content-Length")
	}
	return Outcome{
		Worker:     worker,
		Resp:       resp,
		Body:       body,
		StatusCode: resp.StatusCode,
		Header:     header,
		Latency:    latency,
	}
}

// readBounded reads at most max bytes from r. It returns oversize=true when
// more bytes were available, without ever buffering an unbounded response.
func readBounded(r io.Reader, max int64) ([]byte, bool, error) {
	if max <= 0 {
		max = DefaultMaxBodyBytes
	}
	// One byte past the limit so "too large" is detectable.
	b, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(b)) > max {
		return nil, true, nil
	}
	return b, false, nil
}

// decodeBody decompresses a Content-Encoding'd body. handled=false means the
// bytes are returned unchanged (identity, unknown encoding, or a decoding
// error: best effort). oversize=true means the decoded body exceeded max.
func decodeBody(header http.Header, body []byte, max int64) (out []byte, handled, oversize bool) {
	enc := strings.ToLower(strings.TrimSpace(header.Get("Content-Encoding")))
	switch enc {
	case "", "identity":
		return body, false, false
	}
	decoded, err := decompress(enc, body, max)
	if errors.Is(err, errBodyTooLarge) {
		return nil, true, true
	}
	if err != nil {
		return body, false, false
	}
	return decoded, true, false
}

func decompress(enc string, body []byte, max int64) ([]byte, error) {
	switch enc {
	case "gzip", "x-gzip":
		zr, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		defer zr.Close()
		return readDecoded(zr, max)
	case "deflate":
		// RFC 1950 (zlib-wrapped) is what most servers send; some send raw
		// RFC 1951 streams, so fall back.
		if out, err := func() ([]byte, error) {
			zr, err := zlib.NewReader(bytes.NewReader(body))
			if err != nil {
				return nil, err
			}
			defer zr.Close()
			return readDecoded(zr, max)
		}(); err == nil {
			return out, nil
		} else if errors.Is(err, errBodyTooLarge) {
			return nil, err
		}
		fr := flate.NewReader(bytes.NewReader(body))
		defer fr.Close()
		return readDecoded(fr, max)
	default:
		return nil, fmt.Errorf("unsupported content-encoding %q", enc)
	}
}

func readDecoded(r io.Reader, max int64) ([]byte, error) {
	if max <= 0 {
		max = DefaultMaxBodyBytes
	}
	out, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(out)) > max {
		return nil, errBodyTooLarge
	}
	return out, nil
}

// Describe renders a short human-readable form of an error for reports.
// Transport errors quote the request URL (credentials included), so the text
// is scrubbed first.
func Describe(err error) string {
	if err == nil {
		return ""
	}
	msg := redact.Text(err.Error())
	if len(msg) > 200 {
		msg = msg[:200] + "..."
	}
	return strings.ReplaceAll(msg, "\n", " ")
}
