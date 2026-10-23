// ClawEh
// License: MIT

package config

import "testing"

// Allows matches allow_agents entries against the id by identity (any case),
// and "*"; a nil config or list allows nothing.
func TestSubagentsConfigAllows(t *testing.T) {
	var none *SubagentsConfig
	if none.Allows("bob") {
		t.Error("nil config allows bob")
	}
	if (&SubagentsConfig{}).Allows("bob") {
		t.Error("empty list allows bob")
	}
	for _, tc := range []struct {
		allow []string
		id    string
		want  bool
	}{
		{[]string{"bob"}, "bob", true},
		{[]string{"*"}, "alice", true},
		{[]string{"bob"}, " Bob ", true},
		{[]string{"bob-smith"}, "Bob.Smith", true},
		{[]string{"Bob"}, "bob", true},
		{[]string{"bOB"}, "BoB", true},
		{[]string{"bob"}, "alice", false},
		{[]string{"bob"}, "", false},
	} {
		if got := (&SubagentsConfig{AllowAgents: tc.allow}).Allows(tc.id); got != tc.want {
			t.Errorf("Allows(%q) with %v = %v, want %v", tc.id, tc.allow, got, tc.want)
		}
	}
}

// SameAgentID compares identities: case does not matter, blank names nothing.
func TestSameAgentID(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"Bob", "bob", true},
		{"bOB", "BOB", true},
		{"Alice-Smith", "alice-smith", true},
		{"bob", "alice", false},
		{"", "", false},
		{"", "main", false},
		{" ", "main", false},
	} {
		if got := SameAgentID(tc.a, tc.b); got != tc.want {
			t.Errorf("SameAgentID(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}
