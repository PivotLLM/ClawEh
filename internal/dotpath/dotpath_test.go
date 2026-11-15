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

func TestLookup(t *testing.T) {
	var doc any
	if err := json.Unmarshal([]byte(`{
		"subject": "hi",
		"blank": "",
		"n": null,
		"obj": {},
		"zero": 0,
		"from": {"address": "bob@example.com"},
		"nofrom": null,
		"cc": [],
		"to": [{"emailAddress": {"address": "alice@example.com"}}, {"emailAddress": {"address": "bob@example.com"}}],
		"mixed": [{"id": 1}, {"other": 2}],
		"nokey": [{"other": 1}, {"other": 2}],
		"emptyvals": [{"id": ""}, {"id": []}, {"other": 3}],
		"someempty": [{"id": ""}, {"id": 7}],
		"nested": [[], [{"id": 5}]],
		"list": ["a", ""]
	}`), &doc); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path string
		want string
		p    Presence
	}{
		{"subject", `"hi"`, Found},
		{"zero", `0`, Found},
		{"obj", `{}`, Found},
		{"from.address", `"bob@example.com"`, Found},
		{"blank", `""`, Empty},
		{"n", `null`, Empty},
		{"n.deeper", `null`, Empty},
		{"nofrom.address", `null`, Empty},
		{"cc", `[]`, Empty},
		{"cc.emailAddress.address", `[]`, Empty},
		{"to.emailAddress.address", `["alice@example.com","bob@example.com"]`, Found},
		{"mixed.id", `[1]`, Found},
		{"nokey.id", `null`, Absent},
		{"emptyvals.id", `[]`, Empty},
		{"someempty.id", `[7]`, Found},
		{"nested.id", `[[5]]`, Found},
		{"list", `["a",""]`, Found},
		{"missing", `null`, Absent},
		{"from.missing", `null`, Absent},
		{"subject.deeper", `null`, Absent},
		{"blank.deeper", `null`, Absent},
		{"zero.deeper", `null`, Absent},
	} {
		v, p := Lookup(doc, tc.path)
		got, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != tc.want || p != tc.p {
			t.Errorf("Lookup(%q) = %s, %d; want %s, %d", tc.path, got, p, tc.want, tc.p)
		}
	}
	if v, p := Lookup(doc, ""); v == nil || p != Found {
		t.Errorf(`Lookup("") = %v, %d; want the whole value, Found`, v, p)
	}
	if _, p := Lookup("plain text", "subject"); p != Absent {
		t.Errorf("Lookup over plain text = %d, want Absent", p)
	}
}
