// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package classifier

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/PivotLLM/ClawEh/internal/dotpath"
)

// Substitution (design 4.3): a string argument value that is exactly one
// placeholder is replaced by what it names, with its JSON type, so an id is
// never re-quoted or spliced into other text:
//
//	{{item.<path>}}    the path inside the source's list element
//	{{fetched.<name>}} a fetch field's value
//	{{category}}       the category the model chose (actions only)

const (
	placeholderItem     = "item."
	placeholderFetched  = "fetched."
	placeholderCategory = "category"
)

// placeholder is one parsed {{...}}.
type placeholder struct {
	kind string // placeholderItem, placeholderFetched or placeholderCategory
	arg  string // the path or field name
	text string // as written
}

// parsePlaceholder reports whether s holds a placeholder. ok is false when s
// has no "{{" at all; otherwise err says why s is not exactly one known
// placeholder.
func parsePlaceholder(s string) (p placeholder, ok bool, err error) {
	if !strings.Contains(s, "{{") && !strings.Contains(s, "}}") {
		return placeholder{}, false, nil
	}
	if !strings.HasPrefix(s, "{{") || !strings.HasSuffix(s, "}}") || strings.Count(s, "{{") != 1 || strings.Count(s, "}}") != 1 {
		return placeholder{}, true, fmt.Errorf("a placeholder must be the whole value, such as \"{{item.id}}\"; %q is not", s)
	}
	inner := s[2 : len(s)-2]
	switch {
	case inner == placeholderCategory:
		return placeholder{kind: placeholderCategory, text: s}, true, nil
	case strings.HasPrefix(inner, placeholderItem) && validPath(inner[len(placeholderItem):]):
		return placeholder{kind: placeholderItem, arg: inner[len(placeholderItem):], text: s}, true, nil
	case strings.HasPrefix(inner, placeholderFetched) && inner[len(placeholderFetched):] != "":
		return placeholder{kind: placeholderFetched, arg: inner[len(placeholderFetched):], text: s}, true, nil
	}
	return placeholder{}, true, fmt.Errorf("unknown placeholder %s; use {{item.<path>}}, {{fetched.<name>}} or {{category}}", s)
}

// validPath reports whether p is a non-empty dot-path with no empty segment
// and no whitespace.
func validPath(p string) bool {
	if p == "" || strings.ContainsAny(p, " \t\r\n{}") {
		return false
	}
	for seg := range strings.SplitSeq(p, ".") {
		if seg == "" {
			return false
		}
	}
	return true
}

// walkArgs calls fn for every string value in v (maps and arrays at any
// depth), with its key path for messages, and returns a deep copy of v with
// each string replaced by what fn returns.
func walkArgs(v any, where string, fn func(where, s string) (any, error)) (any, error) {
	switch node := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(node))
		keys := make([]string, 0, len(node))
		for k := range node {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			got, err := walkArgs(node[k], where+"."+k, fn)
			if err != nil {
				return nil, err
			}
			out[k] = got
		}
		return out, nil
	case []any:
		out := make([]any, len(node))
		for i, e := range node {
			got, err := walkArgs(e, fmt.Sprintf("%s[%d]", where, i), fn)
			if err != nil {
				return nil, err
			}
			out[i] = got
		}
		return out, nil
	case string:
		return fn(where, node)
	default:
		return v, nil
	}
}

// checkPlaceholders refuses, one sentence per issue, any placeholder in args
// that is not allowed there: allowed lists the kinds the place may use, and
// fields the fetch fields {{fetched.<name>}} may name.
func checkPlaceholders(args map[string]any, where string, allowed []string, fields map[string]string) []string {
	var issues []string
	check := func(at, s string) (any, error) {
		p, ok, err := parsePlaceholder(s)
		switch {
		case !ok:
		case err != nil:
			issues = append(issues, at+": "+err.Error()+".")
		case !slices.Contains(allowed, p.kind):
			issues = append(issues, at+": "+p.text+" cannot be used in "+where+"; "+allowedText(allowed)+".")
		case p.kind == placeholderFetched:
			if _, ok := fields[p.arg]; !ok {
				issues = append(issues, at+": "+p.text+" names no fetch field.")
			}
		}
		return s, nil
	}
	if _, err := walkArgs(args, where, check); err != nil {
		issues = append(issues, err.Error())
	}
	return issues
}

func allowedText(allowed []string) string {
	if len(allowed) == 0 {
		return "no placeholder is allowed there"
	}
	names := make([]string, 0, len(allowed))
	for _, k := range allowed {
		switch k {
		case placeholderItem:
			names = append(names, "{{item.<path>}}")
		case placeholderFetched:
			names = append(names, "{{fetched.<name>}}")
		case placeholderCategory:
			names = append(names, "{{category}}")
		}
	}
	return "use " + strings.Join(names, ", ")
}

// unresolvedError is a placeholder that resolved to nothing for one item
// (design 4.3b): the item fails, nothing empty is passed to a tool.
type unresolvedError struct {
	placeholder string
}

func (e *unresolvedError) Error() string {
	return e.placeholder + " resolves to nothing for this item"
}

// substitute returns a deep copy of args with every placeholder replaced: item
// paths through dotpath.Get on the list element, fetched names from fetched,
// {{category}} by category. A placeholder resolving to nothing (nil, or an
// unknown fetched name, or no category) fails with *unresolvedError.
func substitute(args map[string]any, item any, fetched map[string]any, category string) (map[string]any, error) {
	if args == nil {
		return nil, nil
	}
	out, err := walkArgs(args, "args", func(_, s string) (any, error) {
		p, ok, perr := parsePlaceholder(s)
		if !ok {
			return s, nil
		}
		if perr != nil {
			return nil, perr // validation refuses this; a hand-edited file can still hold it
		}
		var v any
		switch p.kind {
		case placeholderItem:
			v = dotpath.Get(item, p.arg)
		case placeholderFetched:
			v = fetched[p.arg]
		case placeholderCategory:
			if category != "" {
				v = category
			}
		}
		if v == nil {
			return nil, &unresolvedError{placeholder: p.text}
		}
		return plainJSON(v), nil
	})
	if err != nil {
		return nil, err
	}
	m, ok := out.(map[string]any)
	if !ok {
		return nil, errors.New("arguments are not an object")
	}
	return m, nil
}

// plainJSON converts json.Number values (the source and fetch results are
// decoded with UseNumber, so ids keep their digits) to the float64 an
// ordinary JSON decode gives, which is what tools expect in their arguments.
// A number float64 cannot hold exactly is passed as its string.
func plainJSON(v any) any {
	switch node := v.(type) {
	case json.Number:
		if !strings.ContainsAny(node.String(), ".eE") { // an integer
			i, err := node.Int64()
			if err != nil || i > maxExactInt || i < -maxExactInt {
				return node.String()
			}
			return float64(i)
		}
		if f, err := node.Float64(); err == nil {
			return f
		}
		return node.String()
	case map[string]any:
		out := make(map[string]any, len(node))
		for k, e := range node {
			out[k] = plainJSON(e)
		}
		return out
	case []any:
		out := make([]any, len(node))
		for i, e := range node {
			out[i] = plainJSON(e)
		}
		return out
	default:
		return v
	}
}

// maxExactInt is the largest integer a float64 holds exactly (2^53).
const maxExactInt = 1 << 53
