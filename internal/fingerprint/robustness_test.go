package fingerprint

import (
	"encoding/json"
	"strings"
	"testing"
)

// Numbers that denote the same value must normalize identically so a
// formatting difference never fails a run.
func TestNormalizeNumericFormsEqual(t *testing.T) {
	cases := []struct {
		a, b string
	}{
		{`{"n":1}`, `{"n":1.0}`},
		{`{"n":1}`, `{"n":1e0}`},
		{`{"n":1.5}`, `{"n":1.50}`},
		{`{"n":1.5}`, `{"n":15e-1}`},
		{`{"n":0.001}`, `{"n":1e-3}`},
		{`{"n":-2}`, `{"n":-2.0}`},
		{`{"n":100}`, `{"n":1e2}`},
		{`{"n":123456789012345678901234567890}`, `{"n":123456789012345678901234567890.0}`},
		{`{"n":0}`, `{"n":-0.0}`},
	}
	for _, c := range cases {
		na := mustNormalize(t, c.a)
		nb := mustNormalize(t, c.b)
		if string(na.Canonical) != string(nb.Canonical) {
			t.Errorf("numeric forms must normalize equal:\n  %s -> %s\n  %s -> %s", c.a, na.Canonical, c.b, nb.Canonical)
		}
	}
}

func TestNormalizeNumericFormsDiffer(t *testing.T) {
	cases := [][2]string{
		{`{"n":1}`, `{"n":2}`},
		{`{"n":1.5}`, `{"n":1.6}`},
		{`{"n":100}`, `{"n":101}`},
		{`{"n":-1}`, `{"n":1}`},
	}
	for _, c := range cases {
		na := mustNormalize(t, c[0])
		nb := mustNormalize(t, c[1])
		if string(na.Canonical) == string(nb.Canonical) {
			t.Errorf("distinct numbers must stay distinct: %s and %s both -> %s", c[0], c[1], na.Canonical)
		}
	}
}

// Trailing garbage after a JSON document must not be silently dropped.
func TestNormalizeTrailingDataNotJSON(t *testing.T) {
	for _, body := range []string{
		`{"a":1}{"b":2}`,
		`{"a":1} trailing`,
		`[1,2] [3]`,
	} {
		n := mustNormalize(t, body)
		if n.IsJSON {
			t.Errorf("body %q must not be treated as a single JSON document", body)
		}
		if string(n.Canonical) != body {
			t.Errorf("body %q must keep raw bytes, got %q", body, n.Canonical)
		}
	}
}

// Volatile keys are dropped at any depth regardless of separator style.
func TestNormalizeDropsVolatileKeys(t *testing.T) {
	base := `{"id":"ord_1","timestamp":"2026-01-01T00:00:00Z","request_id":"r1","meta":{"traceId":"t1","span_id":"s1","Correlation-ID":"c1"},"items":[{"ts":1}]}`
	other := `{"id":"ord_1","timestamp":"2026-06-06T11:11:11Z","requestId":"r2","meta":{"trace_id":"t2","spanId":"s2","correlationid":"c2"},"items":[{"ts":1}]}`

	na := mustNormalize(t, base)
	nb := mustNormalize(t, other)
	if string(na.Canonical) != string(nb.Canonical) {
		t.Errorf("volatile keys must be ignored:\n  %s\n  %s", na.Canonical, nb.Canonical)
	}
}

func TestNormalizeKeepsStableKeys(t *testing.T) {
	a := mustNormalize(t, `{"id":"ord_1","amount":10}`)
	b := mustNormalize(t, `{"id":"ord_2","amount":10}`)
	if string(a.Canonical) == string(b.Canonical) {
		t.Error("stable keys must never be dropped")
	}
}

// Trailing-data guard must not break normal pretty-printed JSON.
func TestNormalizePrettyJSONStillParses(t *testing.T) {
	body := "{\n  \"a\": 1,\n  \"b\": [1, 2]\n}\n"
	n := mustNormalize(t, body)
	if !n.IsJSON {
		t.Fatalf("pretty JSON must parse, got raw fallback: %s", n.Canonical)
	}
}

func TestValuesEqualNumericLiteralForms(t *testing.T) {
	var one, onePointZero json.Number = "1", "1.0"
	if !jsonEqual(one, onePointZero) {
		t.Error("1 and 1.0 must compare equal")
	}
	var two json.Number = "2"
	if jsonEqual(one, two) {
		t.Error("1 and 2 must not compare equal")
	}
	if jsonEqual(one, "1") {
		t.Error("number and string must not compare equal")
	}
}

func TestDefaultIgnoredHeadersIncludeSetCookie(t *testing.T) {
	resp := &Response{
		StatusCode: 200,
		Header: map[string][]string{
			"Set-Cookie":   {"a=1; Path=/", "b=2; Path=/"},
			"Content-Type": {"application/json"},
		},
		Body: []byte(`{"ok":true}`),
	}
	f1, err := Compute(resp, Options{})
	if err != nil {
		t.Fatal(err)
	}
	resp.Header["Set-Cookie"] = []string{"a=9; Path=/"}
	f2, err := Compute(resp, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if f1.Value != f2.Value {
		t.Error("Set-Cookie must not affect the fingerprint")
	}
	if strings.Contains(f1.Value, "a=1") {
		t.Error("cookie material must never appear in a fingerprint")
	}
}

func mustNormalize(t *testing.T, body string) NormalizedBody {
	t.Helper()
	n, err := Normalize([]byte(body), nil)
	if err != nil {
		t.Fatalf("normalize %q: %v", body, err)
	}
	return n
}
