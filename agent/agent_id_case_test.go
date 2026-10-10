// ClawEh
// License: MIT

package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
)

// An install whose agents and folders were lower case keeps working when the
// operator writes the ids with capitals: "Bob" is the agent "bob", in the
// folder bob/ it already has, with the same main conversation, and a binding
// that still says "bob" routes to it.
func TestCapitalisedAgentIDsKeepExistingState(t *testing.T) {
	base := t.TempDir()
	bobDir := filepath.Join(base, "bob")
	if err := os.MkdirAll(filepath.Join(bobDir, "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(bobDir, "MEMORY.md")
	if err := os.WriteFile(marker, []byte("Bob remembers.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			BaseDir: base,
			Defaults: config.AgentDefaults{
				Models:            []string{"test-model"},
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
			List: []config.AgentConfig{
				{ID: "Alice", Name: "Alice", Default: true, Subagents: &config.SubagentsConfig{AllowAgents: []string{"bob"}}},
				{ID: "Bob", Name: "Bob"},
			},
		},
		Bindings: []config.AgentBinding{
			{AgentID: "bob", Match: config.BindingMatch{Channel: "slack"}},
		},
	}
	al := mustNewAgentLoop(t, cfg, bus.NewMessageBus(), &mockProvider{}, nil)

	route, inst, err := al.resolveMessageRoute(bus.InboundMessage{Channel: "slack", SenderID: "u1", ChatID: "c1"})
	if err != nil {
		t.Fatalf("resolveMessageRoute: %v", err)
	}
	if route.AgentID != "bob" || route.SessionKey != "agent:bob:main" {
		t.Fatalf("route = %q %q, want bob agent:bob:main", route.AgentID, route.SessionKey)
	}
	if inst.ID != "bob" || inst.Workspace != bobDir || inst.StateDir != bobDir {
		t.Fatalf("instance id %q workspace %q state %q, want bob in %s", inst.ID, inst.Workspace, inst.StateDir, bobDir)
	}
	if b, rerr := os.ReadFile(marker); rerr != nil || string(b) != "Bob remembers.\n" {
		t.Fatalf("existing MEMORY.md = %q, %v", b, rerr)
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() == "Bob" || e.Name() == "Alice" {
			t.Errorf("a folder %q was created beside the lower-case one", e.Name())
		}
	}
	if alice, ok := al.GetRegistry().Get("ALICE"); !ok || alice.ID != "alice" {
		t.Fatalf("Get(ALICE) = %v, %v", alice, ok)
	}
	if !cfg.Agents.List[0].Subagents.Allows(inst.ID) {
		t.Error("Alice's allow_agents [bob] does not allow Bob")
	}
}
