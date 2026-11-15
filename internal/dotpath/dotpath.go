// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

// Package dotpath selects values from decoded JSON by a dot-separated path.
// It is the one path rule shared by scheduled watches and classifiers, and
// imports only the standard library so packages that must stay free of
// ClawEh imports (the classifier core) can use it.
package dotpath

import "strings"

// Get walks a dot-path through a decoded JSON value and returns what it
// selects.
//
// A path segment indexes an object. When the path meets an array, the
// remaining path is applied to every element and the results are collected,
// so "messages.id" over {"messages":[{"id":1},{"id":2}]} yields [1,2]. That
// fan-out is what makes a single path express "the set of things currently
// there".
//
// An empty path selects v itself. A path that does not resolve returns nil.
func Get(v any, path string) any {
	if path == "" {
		return v
	}
	return getSegments(v, strings.Split(path, "."))
}

func getSegments(v any, segs []string) any {
	if len(segs) == 0 {
		return v
	}
	switch node := v.(type) {
	case map[string]any:
		next, ok := node[segs[0]]
		if !ok {
			return nil
		}
		return getSegments(next, segs[1:])
	case []any:
		// Fan out: apply the remaining path to each element.
		out := make([]any, 0, len(node))
		for _, item := range node {
			if got := getSegments(item, segs); got != nil {
				out = append(out, got)
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	default:
		return nil
	}
}

// Presence is what a path finds in a value (Lookup).
type Presence int

// Presence values.
const (
	// Absent: the path is not in the value. A key along it does not
	// exist, or it meets a string, number or boolean where an object is
	// expected, or none of the elements of a non-empty array has it.
	Absent Presence = iota
	// Empty: the path is there but holds nothing. It meets JSON null or
	// an empty array anywhere along it, or ends at an empty string, or
	// every element of an array that has it holds nothing.
	Empty
	// Found: the path selects a value that is not empty.
	Found
)

// Lookup is Get that also says whether the path is in v at all, so a
// caller can tell a field that is there but empty from one that is not
// there (a wrong path, a field the tool did not return).
//
// Rules, applied segment by segment:
//   - an object: a missing key is Absent; otherwise the walk goes on with
//     its value;
//   - JSON null, wherever it is met, is Empty;
//   - an empty array, wherever it is met, is Empty;
//   - with path left, a non-empty array fans out: the rest of the path is
//     applied to each element, and the result is the list of the values
//     the elements found (Found); with none found, Empty when at least one
//     element held the path but empty, else Absent;
//   - with path left, any other value (string, number, boolean) is Absent;
//   - at the end of the path, an empty string is Empty and anything else
//     (an empty object included) is Found.
//
// The value returned is what was selected: for Found the value (a fan-out
// collects only the elements' found values), for Empty the empty value met
// (nil, "" or an empty list), for Absent nil.
func Lookup(v any, path string) (any, Presence) {
	if path == "" {
		return lookupSegments(v, nil)
	}
	return lookupSegments(v, strings.Split(path, "."))
}

func lookupSegments(v any, segs []string) (any, Presence) {
	switch node := v.(type) {
	case nil:
		return nil, Empty
	case []any:
		if len(node) == 0 {
			return node, Empty
		}
		if len(segs) == 0 {
			return node, Found
		}
		out := make([]any, 0, len(node))
		empty := false
		for _, item := range node {
			got, p := lookupSegments(item, segs)
			switch p {
			case Found:
				out = append(out, got)
			case Empty:
				empty = true
			case Absent: // this element lacks the path; others may have it
			}
		}
		switch {
		case len(out) > 0:
			return out, Found
		case empty:
			return []any{}, Empty
		default:
			return nil, Absent
		}
	case map[string]any:
		if len(segs) == 0 {
			return node, Found
		}
		next, ok := node[segs[0]]
		if !ok {
			return nil, Absent
		}
		return lookupSegments(next, segs[1:])
	case string:
		switch {
		case len(segs) > 0:
			return nil, Absent
		case node == "":
			return node, Empty
		default:
			return node, Found
		}
	default:
		if len(segs) > 0 {
			return nil, Absent
		}
		return node, Found
	}
}
