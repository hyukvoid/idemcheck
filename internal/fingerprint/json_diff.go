package fingerprint

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// DiffPaths returns the JSON paths at which two normalized trees differ,
// in deterministic order. Paths look like "$.order_id" or "$.items[0].id".
// A missing key counts as a difference (value nil).
func DiffPaths(a, b any) []string {
	var out []string
	diffWalk(a, b, "$", &out)
	sort.Strings(out)
	return out
}

func diffWalk(a, b any, path string, out *[]string) {
	am, aIsMap := a.(map[string]any)
	bm, bIsMap := b.(map[string]any)
	if aIsMap && bIsMap {
		keys := map[string]struct{}{}
		for k := range am {
			keys[k] = struct{}{}
		}
		for k := range bm {
			keys[k] = struct{}{}
		}
		sorted := make([]string, 0, len(keys))
		for k := range keys {
			sorted = append(sorted, k)
		}
		sort.Strings(sorted)
		for _, k := range sorted {
			diffWalk(am[k], bm[k], path+"."+k, out)
		}
		return
	}

	as, aIsArr := a.([]any)
	bs, bIsArr := b.([]any)
	if aIsArr && bIsArr {
		n := len(as)
		if len(bs) > n {
			n = len(bs)
		}
		for i := 0; i < n; i++ {
			var av, bv any
			if i < len(as) {
				av = as[i]
			}
			if i < len(bs) {
				bv = bs[i]
			}
			diffWalk(av, bv, path+"["+strconv.Itoa(i)+"]", out)
		}
		return
	}

	if !jsonEqual(a, b) {
		*out = append(*out, path)
	}
}

func jsonEqual(a, b any) bool {
	// json.Number compares by string; identical normalization guarantees
	// identical literal formatting for equal values.
	// Numbers compare in canonical form: 1 and 1.0 are the same value.
	if na, ok := a.(json.Number); ok {
		nb, ok := b.(json.Number)
		return ok && canonicalNumber(na) == canonicalNumber(nb)
	}
	if fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b) && fmt.Sprintf("%T", a) == fmt.Sprintf("%T", b) {
		return true
	}
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	if errA != nil || errB != nil {
		return false
	}
	return string(ja) == string(jb)
}

// ValueAt extracts the value at a path produced by DiffPaths.
// Returns (nil, false) when the path does not exist.
func ValueAt(tree any, path string) (any, bool) {
	segs, err := splitPath(path)
	if err != nil {
		return nil, false
	}
	cur := tree
	for _, s := range segs {
		switch {
		case s.key != "":
			m, ok := cur.(map[string]any)
			if !ok {
				return nil, false
			}
			v, ok := m[s.key]
			if !ok {
				return nil, false
			}
			cur = v
		default: // array index
			arr, ok := cur.([]any)
			if !ok || s.index >= len(arr) {
				return nil, false
			}
			cur = arr[s.index]
		}
	}
	return cur, true
}

type pathSeg struct {
	key   string
	index int
}

func splitPath(p string) ([]pathSeg, error) {
	if !strings.HasPrefix(p, "$") {
		return nil, fmt.Errorf("bad path %q", p)
	}
	rest := p[1:]
	var segs []pathSeg
	i := 0
	for i < len(rest) {
		switch rest[i] {
		case '.':
			i++
		case '[':
			j := strings.IndexByte(rest[i:], ']')
			if j < 0 {
				return nil, fmt.Errorf("bad path %q", p)
			}
			idx, err := strconv.Atoi(rest[i+1 : i+j])
			if err != nil {
				return nil, fmt.Errorf("bad index in %q", p)
			}
			segs = append(segs, pathSeg{index: idx})
			i += j + 1
		default:
			j := i
			for j < len(rest) && rest[j] != '.' && rest[j] != '[' {
				j++
			}
			segs = append(segs, pathSeg{key: rest[i:j]})
			i = j
		}
	}
	return segs, nil
}
