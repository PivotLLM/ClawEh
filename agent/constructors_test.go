// ClawEh
// License: MIT

package agent

import (
	"testing"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/providers"
)

// mustNewAgentLoop builds an AgentLoop or fails the test.
func mustNewAgentLoop(
	t *testing.T,
	cfg *config.Config,
	msgBus *bus.MessageBus,
	provider providers.LLMProvider,
	dispatcher *providers.ProviderDispatcher,
) *AgentLoop {
	t.Helper()
	al, err := NewAgentLoop(cfg, msgBus, provider, dispatcher)
	if err != nil {
		t.Fatalf("NewAgentLoop: %v", err)
	}
	return al
}

// mustNewAgentRegistry builds an AgentRegistry or fails the test.
func mustNewAgentRegistry(t *testing.T, cfg *config.Config, provider providers.LLMProvider) *AgentRegistry {
	t.Helper()
	registry, err := NewAgentRegistry(cfg, provider)
	if err != nil {
		t.Fatalf("NewAgentRegistry: %v", err)
	}
	return registry
}

// mustNewAgentInstance builds an AgentInstance or fails the test.
func mustNewAgentInstance(
	t *testing.T,
	agentCfg *config.AgentConfig,
	defaults *config.AgentDefaults,
	cfg *config.Config,
	provider providers.LLMProvider,
) *AgentInstance {
	t.Helper()
	instance, err := NewAgentInstance(agentCfg, defaults, cfg, provider)
	if err != nil {
		t.Fatalf("NewAgentInstance: %v", err)
	}
	return instance
}
