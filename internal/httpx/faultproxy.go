package httpx

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
)

// Deterministic fault modes. The fault proxy injects exactly one controlled
// failure so a run is reproducible — this is fault injection for a
// black-box idempotency test, not a chaos platform.
const (
	// FaultNone forwards everything (used by tests that need the proxy
	// without an injected fault).
	FaultNone = "none"
	// FaultLostResponse forwards every request but closes the connection
	// for the FIRST upstream response that completes: the server did the
	// work, the answer never reached the client. Exactly one response is
	// dropped per proxy lifetime.
	FaultLostResponse = "lost-response"
)

// ValidFault reports whether mode is a known fault mode.
func ValidFault(mode string) bool {
	return mode == FaultNone || mode == FaultLostResponse
}

// hopHeaders are connection-scoped and must not be forwarded.
var hopHeaders = []string{
	"Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
	"Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

// FaultProxy is a minimal local reverse proxy bound to 127.0.0.1 that
// forwards to an upstream target and optionally drops responses. The client
// under test talks to the proxy; the upstream sees the original request
// (method, path, headers including the idempotency key, and body).
type FaultProxy struct {
	base     *url.URL
	listener net.Listener
	server   *http.Server
	client   *http.Client

	// dropOnComplete selects the injected fault: discard the first
	// upstream response that completes. Set once at construction; the
	// fault set is deliberately tiny and fixed (no chaos knobs).
	dropOnComplete bool

	dropped atomic.Int32 // responses discarded without being written
	forward atomic.Int32 // requests relayed to the upstream
}

// StartFaultProxy starts a proxy on a free loopback port that forwards to
// upstream (an absolute http/https URL) and injects the requested fault.
func StartFaultProxy(upstream, mode string) (*FaultProxy, error) {
	if !ValidFault(mode) {
		return nil, fmt.Errorf("unknown fault mode %q (valid: %s, %s)", mode, FaultNone, FaultLostResponse)
	}
	u, err := url.Parse(upstream)
	if err != nil {
		return nil, fmt.Errorf("parse upstream URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("fault proxy requires an http(s) upstream (got %q)", u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("upstream URL has no host")
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen on loopback: %w", err)
	}
	p := &FaultProxy{
		base:     u,
		listener: ln,
		// Never follow redirects here either: a 3xx is evidence for the
		// caller, not something the proxy should act on.
		client: &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		dropOnComplete: mode == FaultLostResponse,
	}
	p.server = &http.Server{Handler: http.HandlerFunc(p.handle)}
	go func() { _ = p.server.Serve(ln) }()
	return p, nil
}

// URL is the client-facing base URL of the proxy, preserving the upstream
// path and query so the request under test is forwarded verbatim.
func (p *FaultProxy) URL() string {
	u := &url.URL{
		Scheme:   "http",
		Host:     p.listener.Addr().String(),
		Path:     p.base.Path,
		RawQuery: p.base.RawQuery,
	}
	return u.String()
}

// Dropped is how many upstream responses were discarded unwritten.
func (p *FaultProxy) Dropped() int { return int(p.dropped.Load()) }

// Forwarded is how many requests reached the upstream.
func (p *FaultProxy) Forwarded() int { return int(p.forward.Load()) }

// Close stops the proxy.
func (p *FaultProxy) Close() {
	if p.server != nil {
		_ = p.server.Close()
	}
}

// handle relays one request. Order matters for the lost-response fault: the
// upstream request must COMPLETED (the server did the work) before the
// response may be dropped, otherwise the fault would be a request drop and
// the idempotency behavior under test would never happen.
func (p *FaultProxy) handle(w http.ResponseWriter, r *http.Request) {
	target := *p.base
	target.Path = r.URL.Path
	target.RawQuery = r.URL.RawQuery

	outReq, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), r.Body)
	if err != nil {
		http.Error(w, "fault proxy: build upstream request: "+err.Error(), http.StatusBadGateway)
		return
	}
	outReq.Header = r.Header.Clone()
	for _, h := range hopHeaders {
		outReq.Header.Del(h)
	}
	outReq.ContentLength = r.ContentLength
	outReq.Host = target.Host

	resp, err := p.client.Do(outReq)
	if err != nil {
		http.Error(w, "fault proxy: upstream error: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	p.forward.Add(1)

	if p.takeDrop() {
		// First completed response: discard the answer without writing a
		// single byte. The client observes a transport failure while the
		// upstream has already processed the request.
		_ = resp.Body.Close()
		dropConnection(w)
		return
	}

	for k, vs := range resp.Header {
		if isHopHeader(k) {
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// takeDrop claims the single drop slot (lost-response mode drops exactly
// one response; none mode never does).
func (p *FaultProxy) takeDrop() bool {
	if !p.dropOnComplete {
		return false
	}
	return p.dropped.CompareAndSwap(0, 1)
}

// dropConnection removes the client connection without writing a response.
func dropConnection(w http.ResponseWriter) {
	if hj, ok := w.(http.Hijacker); ok {
		if conn, _, err := hj.Hijack(); err == nil {
			_ = conn.Close()
			return
		}
	}
	// No hijack available (e.g. HTTP/2): aborting the handler without a
	// response closes the stream; net/http swallows this sentinel.
	panic(http.ErrAbortHandler)
}

func isHopHeader(k string) bool {
	for _, h := range hopHeaders {
		if strings.EqualFold(k, h) {
			return true
		}
	}
	return false
}
