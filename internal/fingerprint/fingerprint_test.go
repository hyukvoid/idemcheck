package fingerprint

import (
	"net/http"
	"testing"
)

// Canonical comparison must not depend on object key ordering.
func TestNormalizeKeyOrderIrrelevant(t *testing.T) {
	a := []byte(`{"order_id":812,"status":"created"}`)
	b := []byte(`{"status":"created","order_id":812}`)
	na, err := Normalize(a, nil)
	if err != nil {
		t.Fatal(err)
	}
	nb, err := Normalize(b, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(na.Canonical) != string(nb.Canonical) {
		t.Fatalf("canonical forms differ:\n%s\n%s", na.Canonical, nb.Canonical)
	}
}

func TestNormalizeIgnoresConfiguredPaths(t *testing.T) {
	a := []byte(`{"order_id":1,"request_id":"r1","meta":{"trace_id":"t1"}}`)
	b := []byte(`{"request_id":"r2","meta":{"trace_id":"t2"},"order_id":1}`)
	paths := []string{"$.request_id", "$.meta.trace_id"}
	na, err := Normalize(a, paths)
	if err != nil {
		t.Fatal(err)
	}
	nb, err := Normalize(b, paths)
	if err != nil {
		t.Fatal(err)
	}
	if string(na.Canonical) != string(nb.Canonical) {
		t.Fatalf("ignored paths still differ: %s vs %s", na.Canonical, nb.Canonical)
	}
	if DiffPaths(na.Tree, nb.Tree) != nil && len(DiffPaths(na.Tree, nb.Tree)) != 0 {
		t.Fatalf("expected no differences, got %v", DiffPaths(na.Tree, nb.Tree))
	}
}

func TestNormalizeIgnoresArrayWildcard(t *testing.T) {
	a := []byte(`{"items":[{"id":1,"ts":"x"},{"id":2,"ts":"y"}]}`)
	b := []byte(`{"items":[{"id":1,"ts":"xx"},{"id":2,"ts":"yy"}]}`)
	na, err := Normalize(a, []string{"$.items[*].ts"})
	if err != nil {
		t.Fatal(err)
	}
	nb, err := Normalize(b, []string{"$.items[*].ts"})
	if err != nil {
		t.Fatal(err)
	}
	if string(na.Canonical) != string(nb.Canonical) {
		t.Fatalf("wildcard ignore failed: %s vs %s", na.Canonical, nb.Canonical)
	}
}

// Non-JSON bodies must fall back, never crash.
func TestNormalizeMalformedFallsBack(t *testing.T) {
	na, err := Normalize([]byte("not-json{{{"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if na.IsJSON {
		t.Fatal("expected non-JSON fallback")
	}
	if string(na.Canonical) != "not-json{{{" {
		t.Fatalf("fallback must preserve raw body, got %q", na.Canonical)
	}
}

func TestNormalizeInvalidIgnorePath(t *testing.T) {
	if _, err := Normalize([]byte(`{}`), []string{"request_id"}); err == nil {
		t.Fatal("expected error for path not starting with $")
	}
}

// Fingerprint: differing order_id must produce different values; volatile
// fields marked ignored must not.
func TestFingerprintStability(t *testing.T) {
	opts := Options{IgnoreJSONPaths: []string{"$.timestamp"}}
	mk := func(status int, body string) *Response {
		h := http.Header{}
		h.Set("Content-Type", "application/json")
		h.Set("Date", "Mon, 01 Jan 2024 00:00:00 GMT") // volatile, ignored by default
		return &Response{StatusCode: status, Header: h, Body: []byte(body)}
	}

	f1, err := Compute(mk(201, `{"order_id":812,"timestamp":"2024-01-01T00:00:01Z"}`), opts)
	if err != nil {
		t.Fatal(err)
	}
	f2, err := Compute(mk(201, `{"timestamp":"2024-01-01T00:00:02Z","order_id":812}`), opts)
	if err != nil {
		t.Fatal(err)
	}
	if f1.Value != f2.Value {
		t.Fatal("same logical response with ignored volatile field must fingerprint equal")
	}

	f3, err := Compute(mk(201, `{"order_id":813,"timestamp":"2024-01-01T00:00:01Z"}`), opts)
	if err != nil {
		t.Fatal(err)
	}
	if f1.Value == f3.Value {
		t.Fatal("different order_id must fingerprint differently")
	}

	f4, err := Compute(mk(200, `{"order_id":812,"timestamp":"2024-01-01T00:00:01Z"}`), opts)
	if err != nil {
		t.Fatal(err)
	}
	if f1.Value == f4.Value {
		t.Fatal("different status must fingerprint differently")
	}
}

func TestFingerprintIgnoresConfiguredHeaders(t *testing.T) {
	mk := func(trace string) *Response {
		h := http.Header{}
		h.Set("Content-Type", "application/json")
		h.Set("X-Custom-Trace", trace)
		return &Response{StatusCode: 200, Header: h, Body: []byte(`{"ok":true}`)}
	}
	opts := Options{IgnoreHeaders: []string{"x-custom-trace"}}
	f1, _ := Compute(mk("a"), opts)
	f2, _ := Compute(mk("b"), opts)
	if f1.Value != f2.Value {
		t.Fatal("configured ignored header must not affect fingerprint")
	}
	f3, _ := Compute(mk("a"), Options{})
	if f1.Value == f3.Value {
		t.Fatal("header should matter when not ignored")
	}
}
