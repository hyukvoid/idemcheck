package fingerprint

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// NormalizedBody is the canonical form of a response body used for
// semantic comparison. Key ordering never matters: JSON objects are
// re-encoded from Go maps, which encoding/json sorts by key.
type NormalizedBody struct {
	IsJSON bool
	// Tree is the parsed JSON with ignore paths removed (nil when not JSON).
	Tree any
	// Canonical is the deterministic byte representation used for hashing.
	Canonical []byte
}

// Normalize parses body as JSON, removes ignored JSON paths, and produces a
// canonical representation. Non-JSON bodies fall back to the raw bytes so a
// malformed response can never crash the run.
//
// Ignore paths look like "$.request_id", "$.meta.trace_id", "$.items[*].ts".
// Matched object keys are deleted; matched array elements become null so the
// array shape stays comparable.
func Normalize(body []byte, ignorePaths []string) (NormalizedBody, error) {
	patterns, err := compilePatterns(ignorePaths)
	if err != nil {
		return NormalizedBody{}, err
	}

	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || (trimmed[0] != '{' && trimmed[0] != '[') {
		return NormalizedBody{IsJSON: false, Canonical: body}, nil
	}

	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber() // preserve numeric literals; 812 must not become 812.0
	var tree any
	if err := dec.Decode(&tree); err != nil {
		// Body looked like JSON but did not parse: fall back safely.
		return NormalizedBody{IsJSON: false, Canonical: body}, nil
	}

	for _, p := range patterns {
		applyIgnore(tree, p)
	}

	canonical, err := json.Marshal(tree) // map keys are emitted sorted
	if err != nil {
		return NormalizedBody{IsJSON: false, Canonical: body}, nil
	}
	return NormalizedBody{IsJSON: true, Tree: tree, Canonical: canonical}, nil
}

// pattern segment: key != "" means object access, otherwise array access.
type patternSeg struct {
	key     string
	index   int // -1 = wildcard
	isArray bool
}

func compilePatterns(paths []string) ([][]patternSeg, error) {
	out := make([][]patternSeg, 0, len(paths))
	for _, p := range paths {
		segs, err := compilePattern(p)
		if err != nil {
			return nil, err
		}
		out = append(out, segs)
	}
	return out, nil
}

func compilePattern(p string) ([]patternSeg, error) {
	if !strings.HasPrefix(p, "$") {
		return nil, fmt.Errorf("invalid ignore path %q: must start with $", p)
	}
	rest := p[1:]
	if rest == "" {
		return nil, fmt.Errorf("invalid ignore path %q: root $ alone is not allowed", p)
	}
	if strings.HasPrefix(rest, ".") {
		rest = rest[1:]
	}
	var segs []patternSeg
	for _, tok := range strings.Split(rest, ".") {
		if tok == "" {
			return nil, fmt.Errorf("invalid ignore path %q: empty segment", p)
		}
		name := tok
		var indexes string
		if i := strings.IndexByte(tok, '['); i >= 0 {
			name = tok[:i]
			end := strings.IndexByte(tok, ']')
			if end < 0 || end < i {
				return nil, fmt.Errorf("invalid ignore path %q: malformed index in %q", p, tok)
			}
			indexes = tok[i+1 : end]
		}
		if name != "" {
			segs = append(segs, patternSeg{key: name})
		}
		switch {
		case indexes == "":
			// plain key only
		case indexes == "*":
			segs = append(segs, patternSeg{index: -1, isArray: true})
		default:
			idx, err := parseIndex(indexes)
			if err != nil {
				return nil, fmt.Errorf("invalid ignore path %q: %v", p, err)
			}
			segs = append(segs, patternSeg{index: idx, isArray: true})
		}
	}
	if len(segs) == 0 {
		return nil, fmt.Errorf("invalid ignore path %q: no segments", p)
	}
	return segs, nil
}

func parseIndex(s string) (int, error) {
	n := 0
	if s == "" {
		return 0, fmt.Errorf("empty index")
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("bad index %q", s)
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}

// applyIgnore removes nodes matched by segs. It returns true when the node
// itself is fully matched and should be removed by its parent.
func applyIgnore(node any, segs []patternSeg) bool {
	if len(segs) == 0 {
		return true
	}
	s := segs[0]
	rest := segs[1:]
	switch n := node.(type) {
	case map[string]any:
		if s.isArray {
			return false // pattern expects an array here
		}
		v, ok := n[s.key]
		if !ok {
			return false
		}
		if applyIgnore(v, rest) {
			delete(n, s.key)
		}
	case []any:
		if !s.isArray {
			return false // pattern expects an object here
		}
		if s.index == -1 {
			for i, elem := range n {
				if applyIgnore(elem, rest) {
					n[i] = nil // keep array length; null is comparable
				}
			}
			return false
		}
		if s.index < len(n) && applyIgnore(n[s.index], rest) {
			n[s.index] = nil
		}
	}
	return false
}
