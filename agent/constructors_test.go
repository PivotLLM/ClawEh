// ClawEh
// License: MIT

package agent

import (
	"testing"

	"github.com/PivotLLM/ClawEh/agentreg"
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
	opts ...LoopOption,
) *AgentLoop {
	t.Helper()
	al, err := NewAgentLoop(cfg, msgBus, provider, dispatcher, opts...)
	if err != nil {
		t.Fatalf("NewAgentLoop: %v", err)
	}
	// Closing the registry removes a non-owner's private temp root.
	t.Cleanup(al.GetRegistry().Close)
	return al
}

// mustNewAgentRegistry builds a registry of bare instances (no tools, no
// loop) or fails the test.
func mustNewAgentRegistry(t *testing.T, cfg *config.Config, provider providers.LLMProvider) *AgentRegistry {
	t.Helper()
	registry, err := agentreg.New(cfg, agentreg.Hooks[*AgentInstance]{
		Build: func(c *config.Config, spec agentreg.Spec) (*AgentInstance, error) {
			return newAgentInstance(spec, &c.Agents.Defaults, c, provider)
		},
	})
	if err != nil {
		t.Fatalf("agentreg.New: %v", err)
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
