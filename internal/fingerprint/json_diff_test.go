package fingerprint

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestDiffPathsFindsChangedID(t *testing.T) {
	var a, b any
	mustUnmarshal(t, `{"order_id":812,"status":"created"}`, &a)
	mustUnmarshal(t, `{"order_id":813,"status":"created"}`, &b)
	got := DiffPaths(a, b)
	want := []string{"$.order_id"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestDiffPathsNested(t *testing.T) {
	var a, b any
	mustUnmarshal(t, `{"meta":{"trace":"1"},"status":"ok"}`, &a)
	mustUnmarshal(t, `{"meta":{"trace":"2"},"status":"ok"}`, &b)
	got := DiffPaths(a, b)
	want := []string{"$.meta.trace"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestDiffPathsArrayElement(t *testing.T) {
	var a, b any
	mustUnmarshal(t, `{"items":[{"id":1},{"id":2}]}`, &a)
	mustUnmarshal(t, `{"items":[{"id":1},{"id":9}]}`, &b)
	got := DiffPaths(a, b)
	want := []string{"$.items[1].id"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestDiffPathsMissingKey(t *testing.T) {
	var a, b any
	mustUnmarshal(t, `{"x":1}`, &a)
	mustUnmarshal(t, `{"x":1,"y":2}`, &b)
	got := DiffPaths(a, b)
	want := []string{"$.y"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestDiffPathsIdentical(t *testing.T) {
	var a, b any
	mustUnmarshal(t, `{"a":[1,2,{"b":null}]}`, &a)
	mustUnmarshal(t, `{"a":[1,2,{"b":null}]}`, &b)
	if got := DiffPaths(a, b); len(got) != 0 {
		t.Fatalf("expected no diffs, got %v", got)
	}
}

func TestValueAt(t *testing.T) {
	var a any
	mustUnmarshal(t, `{"order_id":812,"items":[{"id":7}]}`, &a)

	v, ok := ValueAt(a, "$.order_id")
	if !ok {
		t.Fatal("expected value at $.order_id")
	}
	// json.Unmarshal decodes numbers as float64; Normalize (production path)
	// uses json.Number, so accept either representation.
	switch n := v.(type) {
	case float64:
		if n != 812 {
			t.Fatalf("got %v, want 812", n)
		}
	case json.Number:
		if n.String() != "812" {
			t.Fatalf("got %s, want 812", n)
		}
	default:
		t.Fatalf("unexpected type %T for %#v", v, v)
	}
	if _, ok := ValueAt(a, "$.items[0].id"); !ok {
		t.Fatal("expected value at $.items[0].id")
	}
	if _, ok := ValueAt(a, "$.nope"); ok {
		t.Fatal("missing path must report not-ok")
	}
}

func mustUnmarshal(t *testing.T, s string, v *any) {
	t.Helper()
	if err := json.Unmarshal([]byte(s), v); err != nil {
		t.Fatalf("unmarshal %s: %v", s, err)
	}
}
