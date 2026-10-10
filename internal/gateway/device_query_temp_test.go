package gateway

import (
	"testing"

	"github.com/PivotLLM/ClawEh/agent"
	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/providers"
	"github.com/PivotLLM/ClawEh/routing"
)

// Temporary agents are invisible to devices: agents.list never lists one and
// chat.history cannot read one's conversation.
func TestDeviceQuerierHidesTempAgents(t *testing.T) {
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			BaseDir: t.TempDir(),
			Defaults: config.AgentDefaults{
				Models:            []string{"test-model"},
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
			List: []config.AgentConfig{{ID: "alice", Name: "Alice", Default: true}},
		},
	}
	al, err := agent.NewAgentLoop(cfg, bus.NewMessageBus(), providers.NewUnconfiguredProvider(), nil)
	if err != nil {
		t.Fatalf("NewAgentLoop: %v", err)
	}
	reg := al.GetRegistry()
	clone, err := reg.CreateClone("alice")
	if err != nil {
		t.Fatalf("Create clone: %v", err)
	}
	fresh, err := reg.CreateFresh(config.AgentConfig{Name: "Scratch"})
	if err != nil {
		t.Fatalf("Create fresh: %v", err)
	}

	q := deviceAgentQuerier{al: al}
	agents, defaultID, _ := q.Agents()
	if len(agents) != 1 || agents[0].ID != "alice" || defaultID != "alice" {
		t.Fatalf("agents.list = %+v (default %q), want only alice", agents, defaultID)
	}
	for _, id := range []string{clone, fresh} {
		inst, _ := reg.Get(id)
		key := routing.BuildAgentMainSessionKey(id)
		if err := inst.Sessions.AddMessage(key, "user", "secret"); err != nil {
			t.Fatalf("AddMessage: %v", err)
		}
		if got := q.History(key); got != nil {
			t.Fatalf("chat.history read temporary agent %s: %+v", id, got)
		}
	}
}
