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
