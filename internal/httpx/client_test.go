package httpx

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Bodies over the read limit must be refused, not buffered.
func TestDoRefusesOversizeBody(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), 1<<16) // 64 KiB
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	c := NewClient(5 * time.Second)
	c.SetMaxBody(1024)
	out := c.Do(context.Background(), Spec{Method: http.MethodGet, URL: srv.URL}, "k", 0)
	if out.Err == nil {
		t.Fatal("oversize body must produce an error")
	}
	if !out.Oversize {
		t.Error("oversize must be flagged as Oversize, not a transport error alone")
	}
	if len(out.Body) != 0 {
		t.Errorf("oversize body must not be retained, got %d bytes", len(out.Body))
	}
}

// Bodies within the limit must read normally.
func TestDoReadsBodyWithinLimit(t *testing.T) {
	payload := bytes.Repeat([]byte("y"), 2048)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	c := NewClient(5 * time.Second)
	c.SetMaxBody(1024 * 1024)
	out := c.Do(context.Background(), Spec{Method: http.MethodGet, URL: srv.URL}, "k", 0)
	if out.Err != nil {
		t.Fatalf("unexpected error: %v", out.Err)
	}
	if !bytes.Equal(out.Body, payload) {
		t.Errorf("body mismatch: got %d bytes", len(out.Body))
	}
}

// Gzip responses must arrive decoded, with stale encodings removed.
func TestDoDecodesGzip(t *testing.T) {
	plain := `{"ok":true,"id":"ord_1"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		_, _ = gz.Write([]byte(plain))
		_ = gz.Close()
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(buf.Bytes())
	}))
	defer srv.Close()

	c := NewClient(5 * time.Second)
	out := c.Do(context.Background(), Spec{Method: http.MethodGet, URL: srv.URL}, "k", 0)
	if out.Err != nil {
		t.Fatalf("unexpected error: %v", out.Err)
	}
	if string(out.Body) != plain {
		t.Errorf("gzip must decode to %q, got %q", plain, out.Body)
	}
	if got := out.Header.Get("Content-Encoding"); got != "" {
		t.Errorf("Content-Encoding must be cleared after decode, got %q", got)
	}
	if got := out.Header.Get("Content-Length"); got != "" {
		t.Errorf("stale Content-Length must be cleared, got %q", got)
	}
}

// Deflate (zlib) responses must decode too.
func TestDoDecodesDeflate(t *testing.T) {
	plain := `{"ok":true}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var buf bytes.Buffer
		zw := zlib.NewWriter(&buf)
		_, _ = zw.Write([]byte(plain))
		_ = zw.Close()
		w.Header().Set("Content-Encoding", "deflate")
		_, _ = w.Write(buf.Bytes())
	}))
	defer srv.Close()

	c := NewClient(5 * time.Second)
	out := c.Do(context.Background(), Spec{Method: http.MethodGet, URL: srv.URL}, "k", 0)
	if out.Err != nil {
		t.Fatalf("unexpected error: %v", out.Err)
	}
	if string(out.Body) != plain {
		t.Errorf("deflate must decode to %q, got %q", plain, out.Body)
	}
}

// A decompression bomb must hit the same limit as a plain oversize body.
func TestDoDecodedGzipEnforcesLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		_, _ = gz.Write(bytes.Repeat([]byte("z"), 1<<20)) // 1 MiB decompressed
		_ = gz.Close()
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(buf.Bytes())
	}))
	defer srv.Close()

	c := NewClient(5 * time.Second)
	c.SetMaxBody(1024)
	out := c.Do(context.Background(), Spec{Method: http.MethodGet, URL: srv.URL}, "k", 0)
	if out.Err == nil || !out.Oversize {
		t.Fatalf("decoded oversize must be flagged, got err=%v oversize=%v", out.Err, out.Oversize)
	}
}

// We ask for identity encoding by default so responses are comparable.
func TestDoRequestsIdentityByDefault(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Accept-Encoding")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := NewClient(5 * time.Second)
	out := c.Do(context.Background(), Spec{Method: http.MethodGet, URL: srv.URL}, "k", 0)
	if out.Err != nil {
		t.Fatalf("unexpected error: %v", out.Err)
	}
	if got != "identity" {
		t.Errorf("Accept-Encoding must default to identity, got %q", got)
	}
}

// Redirects are never followed: the 3xx itself is the observation.
func TestDoDoesNotFollowRedirects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/final" {
			_, _ = w.Write([]byte(`{"reached":true}`))
			return
		}
		http.Redirect(w, r, "/final", http.StatusFound)
	}))
	defer srv.Close()

	c := NewClient(5 * time.Second)
	out := c.Do(context.Background(), Spec{Method: http.MethodGet, URL: srv.URL}, "k", 0)
	if out.Err != nil {
		t.Fatalf("unexpected error: %v", out.Err)
	}
	if out.StatusCode != http.StatusFound {
		t.Errorf("redirect must not be followed, got status %d", out.StatusCode)
	}
}

// The Idempotency-Key header must survive intact on every attempt.
func TestBuildRequestSetsKeyHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(r.Header.Get("Idempotency-Key")))
	}))
	defer srv.Close()

	c := NewClient(5 * time.Second)
	out := c.Do(context.Background(), Spec{Method: http.MethodPost, URL: srv.URL, KeyHeader: "Idempotency-Key"}, "key-abc", 0)
	if out.Err != nil {
		t.Fatalf("unexpected error: %v", out.Err)
	}
	if string(out.Body) != "key-abc" {
		t.Errorf("key header must be sent verbatim, got %q", out.Body)
	}
}
