package agent

import (
	"testing"

	"github.com/PivotLLM/ClawEh/config"
)

// TestBuildMessageManagers_TracksConfig guards the reload fix: a manager exists
// only for agents whose message-token window is > 0, and rebuilding against a changed
// config follows the new config (an agent disabled in the new config loses its
// manager, so its old token no longer validates and none is injected).
func TestBuildMessageManagers_TracksConfig(t *testing.T) {
	mk := func(agents []config.AgentConfig) *config.Config {
		return &config.Config{Agents: config.AgentsConfig{
			BaseDir: t.TempDir(),
			Defaults: config.AgentDefaults{
				Models: []string{"gpt-4"}, MaxTokens: 8192, MaxToolIterations: 10,
			},
			List: agents,
		}}
	}

	cfg := mk([]config.AgentConfig{
		{ID: "alice", Default: true, Message: &config.MessageConfig{WindowMinutes: 5, WindowCount: 3}},
		{ID: "bob"}, // no message config → disabled
	})
	reg := mustNewAgentRegistry(t, cfg, &mockRegistryProvider{})
	m := buildMessageManagers(reg, cfg)
	if _, ok := m["alice"]; !ok {
		t.Error("alice (window>0) should have a message-token manager")
	}
	if _, ok := m["bob"]; ok {
		t.Error("bob (no message config) must not have a manager")
	}

	// Config change: alice disabled, bob enabled. A rebuild must follow it.
	cfg2 := mk([]config.AgentConfig{
		{ID: "alice", Default: true},
		{ID: "bob", Message: &config.MessageConfig{WindowMinutes: 5, WindowCount: 3}},
	})
	reg2 := mustNewAgentRegistry(t, cfg2, &mockRegistryProvider{})
	m2 := buildMessageManagers(reg2, cfg2)
	if _, ok := m2["alice"]; ok {
		t.Error("alice must lose its manager after the message endpoint is disabled")
	}
	if _, ok := m2["bob"]; !ok {
		t.Error("bob should gain a manager after the message endpoint is enabled")
	}
}
