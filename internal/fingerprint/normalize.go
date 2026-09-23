package fingerprint

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
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
	if dec.More() {
		// Trailing data after the first value ("{}{}" or "{} garbage"):
		// not a single JSON document, so compare raw bytes.
		return NormalizedBody{IsJSON: false, Canonical: body}, nil
	}

	for _, p := range patterns {
		applyIgnore(tree, p)
	}
	tree = prepare(tree)

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

// prepare canonicalizes every node in place: volatile keys are removed and
// numeric literals are rewritten in one exact decimal form so that 1 and 1.0
// never look like different responses. It returns the (possibly replaced)
// node.
func prepare(node any) any {
	switch n := node.(type) {
	case map[string]any:
		for k, v := range n {
			if volatileKey(k) {
				delete(n, k)
				continue
			}
			n[k] = prepare(v)
		}
		return n
	case []any:
		for i, v := range n {
			n[i] = prepare(v)
		}
		return n
	case json.Number:
		return json.Number(canonicalNumber(n))
	default:
		return node
	}
}

// volatileKeys are JSON key names that routinely change between logically
// identical responses (clocks, tracing). Matched after lowercasing with
// "_", "-" and "." removed, at any depth: "request_id", "requestId" and
// "request-id" are the same key. This is a default; users can add more
// ignore paths via config, but these always apply.
var volatileKeys = map[string]bool{
	"timestamp":     true,
	"requestid":     true,
	"traceid":       true,
	"correlationid": true,
	"spanid":        true,
}

func volatileKey(k string) bool {
	norm := strings.Map(func(r rune) rune {
		if r == '_' || r == '-' || r == '.' {
			return -1
		}
		return r
	}, strings.ToLower(k))
	return volatileKeys[norm]
}

// maxNumericLiteral caps how large a numeric literal is canonicalized;
// longer literals are compared byte-for-byte.
const maxNumericLiteral = 64

// canonicalNumber renders a JSON number literal as one exact plain-decimal
// form so equal values compare equal regardless of spelling (1, 1.0, 1e0).
// The rewrite uses integer string arithmetic, which is exact for every JSON
// literal up to maxNumericLiteral characters.
func canonicalNumber(n json.Number) string {
	s := strings.TrimSpace(n.String())
	if s == "" || len(s) > maxNumericLiteral {
		return s
	}
	if !strings.ContainsAny(s, ".eE") {
		if i, err := strconv.ParseInt(s, 10, 64); err == nil {
			return strconv.FormatInt(i, 10)
		}
		if u, err := strconv.ParseUint(s, 10, 64); err == nil {
			return strconv.FormatUint(u, 10)
		}
	}
	mant, expStr := s, ""
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		mant, expStr = s[:i], s[i+1:]
	}
	exp := 0
	if expStr != "" {
		e, err := strconv.Atoi(expStr)
		if err != nil || e > 10000 || e < -10000 {
			return s
		}
		exp = e
	}
	neg := strings.HasPrefix(mant, "-")
	if strings.HasPrefix(mant, "-") || strings.HasPrefix(mant, "+") {
		mant = mant[1:]
	}
	intPart, fracPart := mant, ""
	if i := strings.IndexByte(mant, '.'); i >= 0 {
		intPart, fracPart = mant[:i], mant[i+1:]
	}
	digits := intPart + fracPart
	if digits == "" {
		return s // not a well-formed number; compare bytes
	}
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			return s
		}
	}
	// value == int(digits) * 10^(point-len(digits))
	point := len(intPart) + exp
	lead := 0
	for lead < len(digits)-1 && digits[lead] == '0' {
		lead++
	}
	digits, point = digits[lead:], point-lead
	tail := len(digits)
	for tail > 1 && digits[tail-1] == '0' {
		tail--
	}
	digits = digits[:tail]
	if digits == "0" {
		return "0"
	}
	var b strings.Builder
	if neg {
		b.WriteByte('-')
	}
	switch {
	case point <= 0:
		b.WriteString("0.")
		for i := 0; i < -point; i++ {
			b.WriteByte('0')
		}
		b.WriteString(digits)
	case point >= len(digits):
		b.WriteString(digits)
		for i := len(digits); i < point; i++ {
			b.WriteByte('0')
		}
	default:
		b.WriteString(digits[:point])
		b.WriteByte('.')
		b.WriteString(digits[point:])
	}
	return b.String()
}
