// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// JSON Pointer (RFC 6901) support for `share` and `paths` (DESIGN.md §7.6:
// projection rules). The standard library has no pointer implementation
// and no dependency is added for one.

// errArrayTraversal is returned when a pointer token is applied to an
// array: projections select object members only.
var errArrayTraversal = errors.New("array traversal is not allowed")

// validPointer reports whether p is a syntactically valid JSON Pointer:
// empty (the whole document) or a sequence of "/"-prefixed tokens in which
// "~" appears only as "~0" or "~1".
func validPointer(p string) bool {
	if p == "" {
		return true
	}
	if p[0] != '/' {
		return false
	}
	for i := 0; i < len(p); i++ {
		if p[i] != '~' {
			continue
		}
		if i+1 >= len(p) || (p[i+1] != '0' && p[i+1] != '1') {
			return false
		}
		i++
	}
	return true
}

// pointerTokens splits a valid pointer into its unescaped reference
// tokens. The empty pointer has no tokens.
func pointerTokens(p string) ([]string, error) {
	if !validPointer(p) {
		return nil, fmt.Errorf("invalid JSON pointer %q", p)
	}
	if p == "" {
		return nil, nil
	}
	parts := strings.Split(p[1:], "/")
	for i, t := range parts {
		// RFC 6901 §4: "~1" before "~0", so "~01" decodes to "~1".
		parts[i] = strings.ReplaceAll(strings.ReplaceAll(t, "~1", "/"), "~0", "~")
	}
	return parts, nil
}

// tokensPrefix reports whether a is a token-wise prefix of b (or equal).
func tokensPrefix(a, b []string) bool {
	if len(a) > len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// checkProjection validates a `share` or `paths` allowlist statically
// (DESIGN.md §7.6): every pointer is valid and nonempty (the root cannot be a
// projection member), and no pointer is a prefix of another (overlap). It
// returns one message per offending pointer; an empty list is valid. Used
// by validateStatic. Array traversal cannot be told from an object member
// named by digits until a document is present, so projectOutput checks it.
//
// Overlap is token-wise: "/a" overlaps "/a/b" (and a repeated "/a") but
// not "/ab". The later pointer of an overlapping pair is the one reported.
func checkProjection(pointers []string) []string {
	var msgs []string
	valid := make([][]string, 0, len(pointers))
	validPtr := make([]string, 0, len(pointers))
	for _, p := range pointers {
		if p == "" {
			msgs = append(msgs, "the empty pointer (whole document) cannot be a projection member")
			continue
		}
		toks, err := pointerTokens(p)
		if err != nil {
			msgs = append(msgs, err.Error())
			continue
		}
		overlap := ""
		for i, prev := range valid {
			if tokensPrefix(prev, toks) || tokensPrefix(toks, prev) {
				overlap = validPtr[i]
				break
			}
		}
		if overlap != "" {
			msgs = append(msgs, fmt.Sprintf("JSON pointer %q overlaps %q", p, overlap))
			continue
		}
		valid = append(valid, toks)
		validPtr = append(validPtr, p)
	}
	return msgs
}

// resolvePointer returns the value p points to within doc (a value produced by
// encoding/json into any). found is false when the path does not exist;
// err reports an invalid pointer or a token applied to an array (array
// traversal is not allowed in projections).
func resolvePointer(doc any, p string) (value any, found bool, err error) {
	toks, err := pointerTokens(p)
	if err != nil {
		return nil, false, err
	}
	cur := doc
	for _, t := range toks {
		switch node := cur.(type) {
		case map[string]any:
			next, ok := node[t]
			if !ok {
				return nil, false, nil
			}
			cur = next
		case []any:
			return nil, false, fmt.Errorf("JSON pointer %q: %w", p, errArrayTraversal)
		default:
			return nil, false, nil
		}
	}
	return cur, true, nil
}

// decodeJSONValue decodes exactly one JSON value, keeping numbers as
// json.Number so a projection never rewrites them.
func decodeJSONValue(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("decode JSON: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("decode JSON: unexpected data after the value")
	}
	return v, nil
}

// encodeJSONValue encodes v as compact encoding/json output (object keys
// sorted) without HTML escaping, so text reads as written.
func encodeJSONValue(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("encode JSON: %w", err)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// projectOutput builds the projection of the JSON document doc: a fresh object
// holding only the object members the pointers select, with the enclosing
// object structure kept (a pointer "/a/b" yields {"a":{"b":...}}); a
// selected member may contain whole arrays or objects. An empty pointer
// list yields {}. A pointer that resolves to nothing, or that traverses
// an array, is an error: a missing `share` path fails the output's
// validation, a missing `paths` path fails the route. The pointers must also pass
// checkProjection. The result is compact encoding/json output with sorted
// keys and no HTML escaping.
func projectOutput(doc []byte, pointers []string) ([]byte, error) {
	if msgs := checkProjection(pointers); len(msgs) > 0 {
		return nil, fmt.Errorf("projection: %s", strings.Join(msgs, "; "))
	}
	root, err := decodeJSONValue(doc)
	if err != nil {
		return nil, fmt.Errorf("projection: %w", err)
	}
	out := map[string]any{}
	for _, p := range pointers {
		toks, err := pointerTokens(p)
		if err != nil {
			return nil, fmt.Errorf("projection: %w", err)
		}
		value, found, err := resolvePointer(root, p)
		if err != nil {
			return nil, fmt.Errorf("projection: %w", err)
		}
		if !found {
			return nil, fmt.Errorf("projection: JSON pointer %q selects nothing", p)
		}
		node := out
		for _, t := range toks[:len(toks)-1] {
			child, ok := node[t].(map[string]any)
			if !ok {
				child = map[string]any{}
				node[t] = child
			}
			node = child
		}
		node[toks[len(toks)-1]] = value
	}
	return encodeJSONValue(out)
}
