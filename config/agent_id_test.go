// ClawEh
// License: MIT

package config

import "testing"

// Allows matches allow_agents entries as the runtime does: normalized ids
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
		{[]string{" Bob "}, "bob", true},
		{[]string{"Bob.Smith"}, "bob-smith", true},
		{[]string{"bob"}, "alice", false},
	} {
		if got := (&SubagentsConfig{AllowAgents: tc.allow}).Allows(tc.id); got != tc.want {
			t.Errorf("Allows(%q) with %v = %v, want %v", tc.id, tc.allow, got, tc.want)
		}
	}
}
