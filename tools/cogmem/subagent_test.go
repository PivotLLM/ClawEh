// ClawEh - Cognitive Memory
// License: MIT

package cogmem

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PivotLLM/cogmem/store"

	"github.com/PivotLLM/ClawEh/cogmemhost"
	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/tools"
)

const mainSession = "agent:alice:main"

func handlersFor(deps tools.ToolDeps) map[string]global.ToolHandler {
	defs := GlobalProvider.RegisterTools(global.Deps{Host: deps})
	m := make(map[string]global.ToolHandler, len(defs))
	for _, d := range defs {
		m[d.Name] = d.Handler
	}
	return m
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// TestMemoryLivesInStateDir checks that the tools work on <state dir>/cogmem:
// the workspace when no state directory is set (a config agent), the state
// directory when one is (a temporary agent sharing another's workspace), and
// that two agents sharing a workspace do not see each other's memories.
func TestMemoryLivesInStateDir(t *testing.T) {
	ws := t.TempDir()
	cloneState := filepath.Join(t.TempDir(), "clone")

	alice := handlersFor(tools.ToolDeps{Workspace: ws, AgentID: "alice"})
	clone := handlersFor(tools.ToolDeps{Workspace: ws, StateDir: cloneState, AgentID: "alice"})

	if res := run(t, alice["memory_create"], newCall(mainSession, map[string]any{
		"type": "fact", "text": "Alice prefers coffee.",
	})); res.IsError {
		t.Fatalf("memory_create (alice) failed: %s", res.ForLLM)
	}
	if !fileExists(store.DBPath(cogmemhost.Dir(ws))) {
		t.Fatal("a config agent's memory must live in its workspace")
	}

	if res := run(t, clone["memory_create"], newCall("agent:c1:main", map[string]any{
		"type": "fact", "text": "Bob prefers tea.",
	})); res.IsError {
		t.Fatalf("memory_create (clone) failed: %s", res.ForLLM)
	}
	if !fileExists(store.DBPath(cogmemhost.Dir(cloneState))) {
		t.Fatal("a temporary agent's memory must live in its state directory")
	}

	other := run(t, alice["memory_search"], newCall(mainSession, map[string]any{"query": "Bob prefers tea."}))
	if other.IsError || !strings.Contains(other.ForLLM, "No active memories match") {
		t.Fatalf("alice must not see the clone's memory: %s", other.ForLLM)
	}
	found := run(t, clone["memory_search"], newCall("agent:c1:main", map[string]any{"query": "Bob prefers tea."}))
	if found.IsError || !strings.Contains(found.ForLLM, "Bob prefers tea.") {
		t.Fatalf("the clone should find its own memory: %s", found.ForLLM)
	}
}

// TestEphemeralMemoryDoesNotConsolidate checks that an agent with an ephemeral
// memory (a sub-agent's snapshot) cannot start a consolidation run, and that
// any other agent can.
func TestEphemeralMemoryDoesNotConsolidate(t *testing.T) {
	var triggered []string
	SetConsolidateTrigger(func(_, sessionKey string) { triggered = append(triggered, sessionKey) })
	t.Cleanup(func() { SetConsolidateTrigger(nil) })

	ws := t.TempDir()
	ephemeral := handlersFor(tools.ToolDeps{Workspace: ws, StateDir: t.TempDir(), AgentID: "alice", EphemeralMemory: true})
	run(t, ephemeral["consolidate"], newCall("agent:c1:main", nil))
	if len(triggered) != 0 {
		t.Fatalf("an ephemeral memory must not trigger consolidation, got %v", triggered)
	}

	live := handlersFor(tools.ToolDeps{Workspace: ws, AgentID: "alice"})
	run(t, live["consolidate"], newCall(mainSession, nil))
	if len(triggered) != 1 || triggered[0] != mainSession {
		t.Fatalf("a live memory should trigger consolidation once, got %v", triggered)
	}
}
