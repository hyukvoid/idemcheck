package httpx

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// echoServer reports what it received so forwarding can be asserted
// verbatim: method, path, key header and body must all survive the proxy.
func echoServer(hits *atomic.Int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		body := make([]byte, 4096)
		n, _ := r.Body.Read(body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"method":%q,"path":%q,"key":%q,"body":%q}`,
			r.Method, r.URL.Path, r.Header.Get("Idempotency-Key"), string(body[:n]))
	}))
}

// The lost-response fault must drop exactly ONE completed response: the
// upstream has already processed the request (idempotency work done), but
// the client sees a transport failure instead of an answer.
func TestFaultProxyDropsFirstCompletedResponse(t *testing.T) {
	var hits atomic.Int32
	srv := echoServer(&hits)
	defer srv.Close()

	fp, err := StartFaultProxy(srv.URL+"/orders?x=1", FaultLostResponse)
	if err != nil {
		t.Fatal(err)
	}
	defer fp.Close()

	c := NewClient(3 * time.Second)
	spec := Spec{Method: http.MethodPost, URL: fp.URL(), Body: []byte(`{"item":1}`),
		KeyHeader: "Idempotency-Key", Header: http.Header{}}

	first := c.Do(context.Background(), spec, "key-1", 0)
	if first.Err == nil {
		t.Fatal("dropped response must surface as a transport error")
	}
	if first.Oversize {
		t.Error("a dropped response is not an oversize body")
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("upstream hits = %d, want 1: the server must process the request whose response is dropped", got)
	}
	if fp.Dropped() != 1 {
		t.Errorf("dropped = %d, want 1", fp.Dropped())
	}

	// Exactly one drop: the retry with the same key gets a real answer.
	second := c.Do(context.Background(), spec, "key-1", 1)
	if second.Err != nil {
		t.Fatalf("retry after the drop must succeed, got %v", second.Err)
	}
	if second.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", second.StatusCode)
	}
	var echo map[string]string
	if err := json.Unmarshal(second.Body, &echo); err != nil {
		t.Fatalf("decode echo: %v (%s)", err, second.Body)
	}
	if echo["key"] != "key-1" || echo["body"] != `{"item":1}` || echo["path"] != "/orders" {
		t.Errorf("forwarded request mismatch: %+v", echo)
	}
	if fp.Dropped() != 1 {
		t.Errorf("second response must not be dropped (dropped = %d)", fp.Dropped())
	}
	if hits.Load() != 2 {
		t.Errorf("upstream hits = %d, want 2", hits.Load())
	}
}

// In none mode the proxy is a plain forwarder: useful for asserting that
// the proxy itself never changes verdicts.
func TestFaultProxyNoneModeForwardsEverything(t *testing.T) {
	var hits atomic.Int32
	srv := echoServer(&hits)
	defer srv.Close()

	fp, err := StartFaultProxy(srv.URL, FaultNone)
	if err != nil {
		t.Fatal(err)
	}
	defer fp.Close()

	c := NewClient(3 * time.Second)
	spec := Spec{Method: http.MethodPost, URL: fp.URL(), Body: []byte(`{"item":1}`),
		KeyHeader: "Idempotency-Key", Header: http.Header{}}
	for i := 0; i < 5; i++ {
		out := c.Do(context.Background(), spec, fmt.Sprintf("key-%d", i), i)
		if out.Err != nil {
			t.Fatalf("request %d failed: %v", i, out.Err)
		}
		if out.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", out.StatusCode)
		}
	}
	if fp.Dropped() != 0 {
		t.Errorf("none mode must drop nothing, dropped = %d", fp.Dropped())
	}
	if hits.Load() != 5 {
		t.Errorf("upstream hits = %d, want 5", hits.Load())
	}
}

// Response status and headers must pass through: they are evidence.
func TestFaultProxyPreservesStatusAndHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-Id", "req-7")
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"order_id":401}`)
	}))
	defer srv.Close()

	fp, err := StartFaultProxy(srv.URL, FaultNone)
	if err != nil {
		t.Fatal(err)
	}
	defer fp.Close()

	c := NewClient(3 * time.Second)
	out := c.Do(context.Background(), Spec{Method: http.MethodPost, URL: fp.URL(),
		Body: []byte(`{}`), KeyHeader: "Idempotency-Key", Header: http.Header{}}, "k", 0)
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	if out.StatusCode != http.StatusCreated {
		t.Errorf("status = %d, want 201", out.StatusCode)
	}
	if out.Header.Get("X-Request-Id") != "req-7" {
		t.Errorf("header lost across the proxy: %v", out.Header)
	}
	if !strings.Contains(string(out.Body), "401") {
		t.Errorf("body = %s", out.Body)
	}
}

func TestStartFaultProxyRejectsBadInput(t *testing.T) {
	if _, err := StartFaultProxy("http://127.0.0.1:1", "meltdown"); err == nil {
		t.Error("unknown fault mode must be rejected")
	}
	if _, err := StartFaultProxy("ftp://example.test/x", FaultNone); err == nil {
		t.Error("non-http upstream must be rejected")
	}
}
