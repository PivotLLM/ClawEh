package config

import "testing"

// TestFindAgent: an agent is found by id, else by name, case-insensitively;
// an id wins over another agent's name.
func TestFindAgent(t *testing.T) {
	cfg := &Config{Agents: AgentsConfig{List: []AgentConfig{
		{ID: "alice", Name: "Bob"},
		{ID: "bob", Name: "Alice"},
		{ID: "agent3", Name: "Helper"},
		{ID: "agent4"},
	}}}
	for _, tc := range []struct{ ref, wantID, wantName string }{
		{"alice", "alice", "Bob"},
		{"BOB", "bob", "Alice"},
		{"helper", "agent3", "Helper"},
		{"agent4", "agent4", "agent4"},
		{" ", "", ""},
		{"zed", "", ""},
	} {
		a := cfg.FindAgent(tc.ref)
		if tc.wantID == "" {
			if a != nil {
				t.Fatalf("FindAgent(%q) = %s, want none", tc.ref, a.ID)
			}
			continue
		}
		if a == nil || a.ID != tc.wantID || a.DisplayName() != tc.wantName {
			t.Fatalf("FindAgent(%q) = %+v, want %s (%s)", tc.ref, a, tc.wantID, tc.wantName)
		}
	}
}
