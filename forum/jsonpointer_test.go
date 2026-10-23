// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestValidPointer(t *testing.T) {
	tests := []struct {
		name string
		p    string
		want bool
	}{
		{"empty is the whole document", "", true},
		{"root member with empty name", "/", true},
		{"single member", "/a", true},
		{"nested", "/a/b/c", true},
		{"escaped tilde", "/a~0b", true},
		{"escaped slash", "/a~1b", true},
		{"both escapes", "/~01~10", true},
		{"empty tokens", "//", true},
		{"digits", "/0", true},
		{"no leading slash", "a", false},
		{"no leading slash nested", "a/b", false},
		{"bare tilde at end", "/a~", false},
		{"tilde with bad escape", "/a~2", false},
		{"tilde followed by slash", "/~/a", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := validPointer(tc.p); got != tc.want {
				t.Errorf("ValidPointer(%q) = %v, want %v", tc.p, got, tc.want)
			}
		})
	}
}

func TestPointerTokens(t *testing.T) {
	tests := []struct {
		p    string
		want []string
	}{
		{"", nil},
		{"/", []string{""}},
		{"/a/b", []string{"a", "b"}},
		{"/a~1b", []string{"a/b"}},
		{"/m~0n", []string{"m~n"}},
		// RFC 6901 §4: "~01" decodes to "~1", not "/".
		{"/~01", []string{"~1"}},
		{"/~10", []string{"/0"}},
	}
	for _, tc := range tests {
		got, err := pointerTokens(tc.p)
		if err != nil {
			t.Fatalf("pointerTokens(%q): %v", tc.p, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("pointerTokens(%q) = %q, want %q", tc.p, got, tc.want)
		}
	}
	if _, err := pointerTokens("a"); err == nil {
		t.Error("pointerTokens(\"a\"): want an error")
	}
}

func TestCheckProjection(t *testing.T) {
	tests := []struct {
		name     string
		pointers []string
		// wantBad are substrings, one per expected message, in order.
		wantBad []string
	}{
		{"nil list", nil, nil},
		{"empty list", []string{}, nil},
		{"disjoint members", []string{"/a", "/b/c", "/ab"}, nil},
		{"escaped names are distinct", []string{"/a~1b", "/a/b"}, nil},
		{"empty pointer", []string{""}, []string{"empty pointer"}},
		{"invalid pointer", []string{"a"}, []string{`invalid JSON pointer "a"`}},
		{"bad escape", []string{"/a~2"}, []string{`invalid JSON pointer "/a~2"`}},
		{"child after parent", []string{"/a", "/a/b"}, []string{`"/a/b" overlaps "/a"`}},
		{"parent after child", []string{"/a/b", "/a"}, []string{`"/a" overlaps "/a/b"`}},
		{"duplicate", []string{"/a", "/a"}, []string{`"/a" overlaps "/a"`}},
		{
			"one message per offending pointer",
			[]string{"/a", "", "/a/x", "/b", "/b/y/z"},
			[]string{"empty pointer", `"/a/x" overlaps "/a"`, `"/b/y/z" overlaps "/b"`},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := checkProjection(tc.pointers)
			if len(got) != len(tc.wantBad) {
				t.Fatalf("CheckProjection(%q) = %q, want %d messages", tc.pointers, got, len(tc.wantBad))
			}
			for i, sub := range tc.wantBad {
				if !strings.Contains(got[i], sub) {
					t.Errorf("message %d = %q, want it to contain %q", i, got[i], sub)
				}
			}
		})
	}
}

func TestResolve(t *testing.T) {
	doc, err := decodeJSONValue([]byte(`{
		"a": {"b": {"c": 1}},
		"list": [1, 2, {"x": 3}],
		"s": "text",
		"a/b": "slash",
		"m~n": "tilde",
		"": "empty name",
		"0": "digit name",
		"nil": null
	}`))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		p         string
		wantValue any
		wantFound bool
		wantArray bool
		wantErr   bool
	}{
		{name: "nested member", p: "/a/b/c", wantValue: "1", wantFound: true},
		{name: "object member", p: "/a/b", wantValue: map[string]any{"c": "1"}, wantFound: true},
		{name: "whole array member", p: "/list", wantValue: []any{"1", "2", map[string]any{"x": "3"}}, wantFound: true},
		{name: "escaped slash", p: "/a~1b", wantValue: "slash", wantFound: true},
		{name: "escaped tilde", p: "/m~0n", wantValue: "tilde", wantFound: true},
		{name: "empty member name", p: "/", wantValue: "empty name", wantFound: true},
		{name: "digit member name on an object", p: "/0", wantValue: "digit name", wantFound: true},
		{name: "null member is found", p: "/nil", wantValue: nil, wantFound: true},
		{name: "missing member", p: "/nope"},
		{name: "missing nested member", p: "/a/x/y"},
		{name: "through a scalar", p: "/s/x"},
		{name: "into an array", p: "/list/0", wantErr: true, wantArray: true},
		{name: "through an array", p: "/list/2/x", wantErr: true, wantArray: true},
		{name: "invalid pointer", p: "list", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v, found, rerr := resolvePointer(doc, tc.p)
			if (rerr != nil) != tc.wantErr {
				t.Fatalf("Resolve(%q) err = %v, wantErr %v", tc.p, rerr, tc.wantErr)
			}
			if tc.wantArray && !errors.Is(rerr, errArrayTraversal) {
				t.Errorf("Resolve(%q) err = %v, want errArrayTraversal", tc.p, rerr)
			}
			if found != tc.wantFound {
				t.Fatalf("Resolve(%q) found = %v, want %v", tc.p, found, tc.wantFound)
			}
			if found && !reflect.DeepEqual(ptrNormalise(v), tc.wantValue) {
				t.Errorf("Resolve(%q) = %#v, want %#v", tc.p, v, tc.wantValue)
			}
		})
	}

	whole, found, err := resolvePointer(doc, "")
	if err != nil || !found || !reflect.DeepEqual(whole, doc) {
		t.Errorf("Resolve(doc, \"\") = %v, %v, %v; want the whole document", whole, found, err)
	}
}

// ptrNormalise turns json.Number values into their strings so expectations
// can be written as plain literals.
func ptrNormalise(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = ptrNormalise(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = ptrNormalise(e)
		}
		return out
	case interface{ String() string }:
		return x.String()
	}
	return v
}

func TestProject(t *testing.T) {
	const doc = `{"summary":"ok <b>&</b>","score":12345678901234567890,"ratio":1.50,
		"detail":{"private":"secret","public":{"k":[1,{"z":2}]},"note":"n"},
		"items":[{"a":1}],"a/b":"slash","m~n":"tilde"}`
	tests := []struct {
		name     string
		doc      string
		pointers []string
		want     string
		wantErr  string
	}{
		{name: "empty list publishes nothing", doc: doc, pointers: []string{}, want: `{}`},
		{name: "nil list publishes nothing", doc: doc, pointers: nil, want: `{}`},
		{name: "top-level member", doc: doc, pointers: []string{"/summary"}, want: `{"summary":"ok <b>&</b>"}`},
		{
			name: "enclosing structure kept", doc: doc, pointers: []string{"/detail/public"},
			want: `{"detail":{"public":{"k":[1,{"z":2}]}}}`,
		},
		{
			name: "siblings merged into one parent", doc: doc, pointers: []string{"/detail/note", "/detail/public"},
			want: `{"detail":{"note":"n","public":{"k":[1,{"z":2}]}}}`,
		},
		{name: "whole array member", doc: doc, pointers: []string{"/items"}, want: `{"items":[{"a":1}]}`},
		{name: "numbers kept verbatim", doc: doc, pointers: []string{"/score", "/ratio"}, want: `{"ratio":1.50,"score":12345678901234567890}`},
		{name: "escaped names", doc: doc, pointers: []string{"/a~1b", "/m~0n"}, want: `{"a/b":"slash","m~n":"tilde"}`},
		{name: "missing path fails", doc: doc, pointers: []string{"/summary", "/absent"}, wantErr: `"/absent" selects nothing`},
		{name: "missing nested path fails", doc: doc, pointers: []string{"/detail/absent"}, wantErr: "selects nothing"},
		{name: "array traversal fails", doc: doc, pointers: []string{"/items/0/a"}, wantErr: "array traversal"},
		{name: "array root with pointers fails", doc: `[{"a":1}]`, pointers: []string{"/0"}, wantErr: "array traversal"},
		{name: "overlap fails", doc: doc, pointers: []string{"/detail", "/detail/note"}, wantErr: "overlaps"},
		{name: "empty pointer fails", doc: doc, pointers: []string{""}, wantErr: "empty pointer"},
		{name: "invalid pointer fails", doc: doc, pointers: []string{"summary"}, wantErr: "invalid JSON pointer"},
		{name: "invalid JSON fails", doc: `{"a":`, pointers: []string{"/a"}, wantErr: "decode JSON"},
		{name: "trailing data fails", doc: `{"a":1} {"b":2}`, pointers: []string{"/a"}, wantErr: "after the value"},
		{name: "scalar root with pointers is missing", doc: `"text"`, pointers: []string{"/a"}, wantErr: "selects nothing"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := projectOutput([]byte(tc.doc), tc.pointers)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("Project err = %v, want it to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Project: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("Project = %s, want %s", got, tc.want)
			}
		})
	}
}
