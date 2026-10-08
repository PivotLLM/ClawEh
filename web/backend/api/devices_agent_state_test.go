// ClawEh
// License: MIT

package api

import (
	"testing"

	"github.com/PivotLLM/ClawEh/config"
)

// A device assignment counts only for an agent the service runs; otherwise
// the device view says why, by the rule the running service applies.
func TestAssignedAgentState(t *testing.T) {
	off := false
	cfg := config.DefaultConfig()
	cfg.Providers = []config.Provider{
		{Name: "openai", Protocol: "openai-chat", BaseURL: "https://api.openai.com/v1", APIKey: "sk-test-0123456789"},
		{Name: "People", Protocol: config.HumanProtocol},
	}
	cfg.Models = []config.ModelConfig{
		{ModelName: "m", Model: "gpt-4o", Provider: "openai", Enabled: true},
		{ModelName: "Bob (human)", Model: "bob", Provider: "People", Enabled: true},
		{ModelName: "Carol (human)", Model: "carol", Provider: "People", Enabled: true},
	}
	cfg.Agents.List = []config.AgentConfig{
		{ID: "alice", Name: "Alice", Default: true, Models: []string{"m"}},
		{ID: "bob", Name: "Bob", Models: []string{"Bob (human)"}},
		// A human agent with a second model breaks the rules: set aside.
		{ID: "carol", Name: "Carol", Models: []string{"Carol (human)", "m"}},
		{ID: "dave", Models: []string{"m"}, Enabled: &off},
	}
	cfg.Bindings = []config.AgentBinding{
		{AgentID: "bob", Default: true, Match: config.BindingMatch{Channel: "telegram-main", Peer: &config.PeerMatch{Kind: "direct", ID: "4242"}}},
		{AgentID: "carol", Default: true, Match: config.BindingMatch{Channel: "telegram-main", Peer: &config.PeerMatch{Kind: "direct", ID: "4343"}}},
	}
	for _, tc := range []struct{ ref, name, state string }{
		{"alice", "Alice", ""},
		{"bob", "Bob", ""},
		{"carol", "Carol", "set_aside"},
		{"dave", "dave", "disabled"},
		{"removed", "", "not_found"},
	} {
		name, state := assignedAgentState(cfg, tc.ref)
		if name != tc.name || state != tc.state {
			t.Errorf("%s: (%q, %q), want (%q, %q)", tc.ref, name, state, tc.name, tc.state)
		}
	}
}
