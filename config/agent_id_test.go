// ClawEh
// License: MIT

package config

import "testing"

// Allows matches allow_agents entries (normal form, validateAgentIDs) against
// the normalized id, and "*"; a nil config or list allows nothing.
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
		{[]string{"bob"}, "alice", false},
	} {
		if got := (&SubagentsConfig{AllowAgents: tc.allow}).Allows(tc.id); got != tc.want {
			t.Errorf("Allows(%q) with %v = %v, want %v", tc.id, tc.allow, got, tc.want)
		}
	}
}
