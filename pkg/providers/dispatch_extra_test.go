package providers_test

import (
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/providers"
)

// TestProviderDispatcher_Get_TimeoutInjected verifies that the default timeout
// from Agents.Defaults is injected when the model has no explicit timeout.
func TestProviderDispatcher_Get_TimeoutInjected(t *testing.T) {
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				RequestTimeout: 30,
			},
		},
		ModelList: []config.ModelConfig{
			{
				ModelName:      "test-alias",
				Model:          "claude-cli/test-timeout",
				Enabled:        true,
				RequestTimeout: 0, // No explicit timeout; should inherit from defaults.
			},
		},
	}
	d := providers.NewProviderDispatcher(cfg)

	p, err := d.Get("claude-cli", "test-timeout")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if p == nil {
		t.Fatal("Get() returned nil provider")
	}
}
