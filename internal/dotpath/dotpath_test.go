// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package dotpath

import (
	"encoding/json"
	"testing"
)

func TestGet(t *testing.T) {
	var doc any
	if err := json.Unmarshal([]byte(`{
		"subject": "hi",
		"from": {"address": "bob@example.com"},
		"messages": [{"id": 1}, {"id": 2}, {"other": 3}],
		"empty": [],
		"n": null
	}`), &doc); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path string
		want string
	}{
		{"subject", `"hi"`},
		{"from.address", `"bob@example.com"`},
		{"messages.id", `[1,2]`},
		{"messages", `[{"id":1},{"id":2},{"other":3}]`},
		{"missing", `null`},
		{"from.missing", `null`},
		{"subject.deeper", `null`},
		{"empty.id", `null`},
		{"n", `null`},
	} {
		got, err := json.Marshal(Get(doc, tc.path))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != tc.want {
			t.Errorf("Get(%q) = %s, want %s", tc.path, got, tc.want)
		}
	}
	if got := Get(doc, ""); got == nil {
		t.Error(`Get("") = nil, want the whole value`)
	}
}
