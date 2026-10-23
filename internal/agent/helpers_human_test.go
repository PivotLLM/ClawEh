// ClawEh
// License: MIT

package agent

import (
	"testing"

	"github.com/PivotLLM/ClawEh/config"
)

// `claw agent` applies the same human-agent rules as the gateway: an agent
// breaking them is not run, and a valid one is.
func TestPruneHumanProblems_ClawAgent(t *testing.T) {
	build := func(models []string) *config.Config {
		cfg := config.DefaultConfig()
		cfg.Providers = []config.Provider{
			{Name: "openai", Protocol: "openai-chat", BaseURL: "https://api.openai.com/v1", APIKey: "sk-test"},
			{Name: "People", Protocol: config.HumanProtocol},
		}
		cfg.Models = []config.ModelConfig{
			{ModelName: "m", Model: "gpt-4o", Provider: "openai", Enabled: true},
			{ModelName: "Bob (human)", Model: "bob", Provider: "People", Enabled: true},
		}
		cfg.Agents.List = []config.AgentConfig{
			{ID: "alice", Default: true, Models: []string{"m"}},
			{ID: "bob", Models: models},
		}
		cfg.Bindings = []config.AgentBinding{{
			AgentID: "bob", Default: true,
			Match: config.BindingMatch{Channel: "telegram-main", Peer: &config.PeerMatch{Kind: "direct", ID: "4242"}},
		}}
		return cfg
	}
	valid := build([]string{"Bob (human)"})
	pruneHumanProblems(valid)
	if !valid.Agents.List[1].IsEnabled() {
		t.Error("a valid human agent was disabled")
	}
	invalid := build([]string{"Bob (human)", "m"})
	pruneHumanProblems(invalid)
	if invalid.Agents.List[1].IsEnabled() {
		t.Error("a human agent with a fallback model is still run")
	}
	if !invalid.Agents.List[0].IsEnabled() {
		t.Error("alice was disabled")
	}
}
