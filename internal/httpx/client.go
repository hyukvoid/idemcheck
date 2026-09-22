// Package httpx builds isolated HTTP requests and executes them with
// sane timeouts. Every call gets its own *http.Request so concurrent
// workers never share mutable request state.
package httpx

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

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
	Err         error
}

// Client wraps net/http with per-request timeouts.
type Client struct {
	hc      *http.Client
	timeout time.Duration
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
	}
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

// Do executes one request and reads the full response body.
func (c *Client) Do(ctx context.Context, spec Spec, key string, worker int) Outcome {
	req, err := spec.BuildRequest(ctx, key)
	if err != nil {
		return Outcome{Worker: worker, Err: err}
	}
	start := time.Now()
	resp, err := c.hc.Do(req)
	latency := time.Since(start)
	if err != nil {
		return Outcome{Worker: worker, Latency: latency, Err: err}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return Outcome{Worker: worker, Latency: latency, StatusCode: resp.StatusCode, Err: fmt.Errorf("read body: %w", err)}
	}
	return Outcome{
		Worker:     worker,
		Resp:       resp,
		Body:       body,
		StatusCode: resp.StatusCode,
		Header:     resp.Header.Clone(),
		Latency:    latency,
	}
}

// Describe renders a short human-readable form of an error for reports.
func Describe(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if len(msg) > 200 {
		msg = msg[:200] + "..."
	}
	return strings.ReplaceAll(msg, "\n", " ")
}
