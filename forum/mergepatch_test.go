// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"encoding/json"
	"reflect"
	"testing"
)

// The examples of RFC 7386 Appendix A, and the cases the forum relies on.
func TestMergePatch(t *testing.T) {
	tests := []struct {
		name, target, patch, want string
	}{
		{"replace a member", `{"a":"b"}`, `{"a":"c"}`, `{"a":"c"}`},
		{"add a member", `{"a":"b"}`, `{"b":"c"}`, `{"a":"b","b":"c"}`},
		{"null deletes a member", `{"a":"b"}`, `{"a":null}`, `{}`},
		{"delete one of two", `{"a":"b","b":"c"}`, `{"a":null}`, `{"b":"c"}`},
		{"array replaces a string", `{"a":["b"]}`, `{"a":"c"}`, `{"a":"c"}`},
		{"string replaces an array", `{"a":"c"}`, `{"a":["b"]}`, `{"a":["b"]}`},
		{"nested merge and delete", `{"a":{"b":"c"}}`, `{"a":{"b":"d","c":null}}`, `{"a":{"b":"d"}}`},
		{"array replaced whole", `{"a":[{"b":"c"}]}`, `{"a":[1]}`, `{"a":[1]}`},
		{"arrays", `["a","b"]`, `["c","d"]`, `["c","d"]`},
		{"object patch on an array", `["a","b"]`, `{"a":"b"}`, `{"a":"b"}`},
		{"null on an absent member", `{"a":"foo"}`, `{"b":null}`, `{"a":"foo"}`},
		{"null patch", `{"a":"foo"}`, `null`, `null`},
		{"string patch", `{"a":"foo"}`, `"bar"`, `"bar"`},
		{"a null in the target is kept", `{"e":null}`, `{"a":1}`, `{"e":null,"a":1}`},
		{"object patch with a null on an array", `[1,2]`, `{"a":"b","c":null}`, `{"a":"b"}`},
		{"nulls inside a new object are dropped", `{}`, `{"a":{"bb":{"ccc":null}}}`, `{"a":{"bb":{}}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := mergePatch(json.RawMessage(tt.target), json.RawMessage(tt.patch))
			if err != nil {
				t.Fatal(err)
			}
			var g, w any
			if err := json.Unmarshal(got, &g); err != nil {
				t.Fatalf("result %s: %v", got, err)
			}
			if err := json.Unmarshal([]byte(tt.want), &w); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(g, w) {
				t.Errorf("mergePatch(%s, %s) = %s, want %s", tt.target, tt.patch, got, tt.want)
			}
		})
	}
}

// Members keep their order, new ones are appended, and untouched values
// keep their bytes (a large integer is not rounded).
func TestMergePatchKeepsTheDocument(t *testing.T) {
	got, err := mergePatch(json.RawMessage(`{"version":1,"name":"a","seed":9007199254740993,"layers":[{"id":"x"}]}`),
		json.RawMessage(`{"name":"b","limits":{"max_calls":3}}`))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"version":1,"name":"b","seed":9007199254740993,"layers":[{"id":"x"}],"limits":{"max_calls":3}}`
	if string(got) != want {
		t.Errorf("mergePatch = %s, want %s", got, want)
	}
}

func TestMergePatchRefusesMalformedJSON(t *testing.T) {
	for _, tc := range []struct{ target, patch string }{{`{}`, `{"a":`}, {`{"a":`, `{"a":1}`}} {
		if _, err := mergePatch(json.RawMessage(tc.target), json.RawMessage(tc.patch)); err == nil {
			t.Errorf("mergePatch(%s, %s) succeeded", tc.target, tc.patch)
		}
	}
}
