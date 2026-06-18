package agent

import (
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/config"
)

// TestBuildCallbackManagers_TracksConfig guards the reload fix: a manager exists
// only for agents whose callback window is > 0, and rebuilding against a changed
// config follows the new config (an agent disabled in the new config loses its
// manager, so its old token no longer validates and none is injected).
func TestBuildCallbackManagers_TracksConfig(t *testing.T) {
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
		{ID: "amber", Default: true, Callback: &config.CallbackConfig{WindowMinutes: 5, WindowCount: 3}},
		{ID: "karen"}, // no callback → disabled
	})
	reg := NewAgentRegistry(cfg, &mockRegistryProvider{})
	m := buildCallbackManagers(reg, cfg)
	if _, ok := m["amber"]; !ok {
		t.Error("amber (window>0) should have a callback manager")
	}
	if _, ok := m["karen"]; ok {
		t.Error("karen (no callback) must not have a callback manager")
	}

	// Config change: amber disabled, karen enabled. A rebuild must follow it.
	cfg2 := mk([]config.AgentConfig{
		{ID: "amber", Default: true},
		{ID: "karen", Callback: &config.CallbackConfig{WindowMinutes: 5, WindowCount: 3}},
	})
	reg2 := NewAgentRegistry(cfg2, &mockRegistryProvider{})
	m2 := buildCallbackManagers(reg2, cfg2)
	if _, ok := m2["amber"]; ok {
		t.Error("amber must lose its manager after callbacks disabled")
	}
	if _, ok := m2["karen"]; !ok {
		t.Error("karen should gain a manager after callbacks enabled")
	}
}
