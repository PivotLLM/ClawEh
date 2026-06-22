package agent

import (
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/config"
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
		{ID: "amber", Default: true, Message: &config.MessageConfig{WindowMinutes: 5, WindowCount: 3}},
		{ID: "karen"}, // no message config → disabled
	})
	reg := NewAgentRegistry(cfg, &mockRegistryProvider{})
	m := buildMessageManagers(reg, cfg)
	if _, ok := m["amber"]; !ok {
		t.Error("amber (window>0) should have a message-token manager")
	}
	if _, ok := m["karen"]; ok {
		t.Error("karen (no message config) must not have a manager")
	}

	// Config change: amber disabled, karen enabled. A rebuild must follow it.
	cfg2 := mk([]config.AgentConfig{
		{ID: "amber", Default: true},
		{ID: "karen", Message: &config.MessageConfig{WindowMinutes: 5, WindowCount: 3}},
	})
	reg2 := NewAgentRegistry(cfg2, &mockRegistryProvider{})
	m2 := buildMessageManagers(reg2, cfg2)
	if _, ok := m2["amber"]; ok {
		t.Error("amber must lose its manager after the message endpoint is disabled")
	}
	if _, ok := m2["karen"]; !ok {
		t.Error("karen should gain a manager after the message endpoint is enabled")
	}
}
