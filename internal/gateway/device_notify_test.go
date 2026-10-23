// ClawEh
// License: MIT

package gateway

import (
	"testing"

	"github.com/PivotLLM/ClawEh/agent"
	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/providers"
)

func notifyTestLoop(t *testing.T, bindings []config.AgentBinding) *agent.AgentLoop {
	t.Helper()
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			BaseDir: t.TempDir(),
			Defaults: config.AgentDefaults{
				Models:            []string{"test-model"},
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
			List: []config.AgentConfig{
				{ID: "alice", Name: "Alice", Default: true},
				{ID: "bob", Name: "Bob"},
			},
		},
		Bindings: bindings,
	}
	al, err := agent.NewAgentLoop(cfg, bus.NewMessageBus(), providers.NewUnconfiguredProvider(), nil)
	if err != nil {
		t.Fatalf("NewAgentLoop: %v", err)
	}
	return al
}

// USB device notifications go to the DEFAULT agent's default channel, not to
// another agent's.
func TestDefaultAgentTarget_DefaultAgentsChannel(t *testing.T) {
	al := notifyTestLoop(t, []config.AgentBinding{
		{AgentID: "bob", Default: true, Match: config.BindingMatch{Channel: "slack"}, DeliverTo: "C1"},
		{AgentID: "alice", Default: true, Match: config.BindingMatch{Channel: "telegram"}, DeliverTo: "111"},
	})
	channel, chatID, ok := defaultAgentTarget(al)()
	if !ok || channel != "telegram" || chatID != "111" {
		t.Fatalf("target = %q/%q ok=%v, want telegram/111", channel, chatID, ok)
	}
}

// With no default channel for the default agent there is no target, even when
// another agent has one.
func TestDefaultAgentTarget_NoDefaultChannel(t *testing.T) {
	al := notifyTestLoop(t, []config.AgentBinding{
		{AgentID: "bob", Default: true, Match: config.BindingMatch{Channel: "slack"}, DeliverTo: "C1"},
	})
	if channel, chatID, ok := defaultAgentTarget(al)(); ok {
		t.Fatalf("target = %q/%q, want none", channel, chatID)
	}
}
