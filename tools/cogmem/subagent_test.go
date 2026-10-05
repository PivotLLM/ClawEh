// ClawEh - Cognitive Memory
// License: MIT

package cogmem

import (
	"os"
	"strings"
	"testing"

	"github.com/PivotLLM/cogmem/store"

	"github.com/PivotLLM/ClawEh/cogmemhost"
	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/tools"
)

const (
	mainSession     = "agent:alice:main"
	subagentSession = "agent:alice:subagent:3f2b9c1e"
)

func buildAgentHandlers(t *testing.T) (map[string]global.ToolHandler, string) {
	t.Helper()
	ws := t.TempDir()
	defs := GlobalProvider.RegisterTools(global.Deps{
		Host: tools.ToolDeps{Workspace: ws, AgentID: "alice"},
	})
	m := make(map[string]global.ToolHandler, len(defs))
	for _, d := range defs {
		m[d.Name] = d.Handler
	}
	return m, ws
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// TestMemoryWritesFollowSession checks that a memory tool writes to the
// sub-agent's snapshot in a sub-agent session and to the agent's own memory in
// any other session, and that neither write is visible from the other side.
func TestMemoryWritesFollowSession(t *testing.T) {
	tests := []struct {
		name       string
		writer     string
		other      string
		wantDir    func(ws string) string
		absentDir  func(ws string) string
		memoryText string
	}{
		{
			name:       "sub-agent write lands in the snapshot",
			writer:     subagentSession,
			other:      mainSession,
			wantDir:    func(ws string) string { return cogmemhost.SubagentDir(ws, subagentSession) },
			absentDir:  cogmemhost.Dir,
			memoryText: "Bob prefers tea.",
		},
		{
			name:       "main-session write lands in the primary memory",
			writer:     mainSession,
			other:      subagentSession,
			wantDir:    cogmemhost.Dir,
			absentDir:  func(ws string) string { return cogmemhost.SubagentDir(ws, subagentSession) },
			memoryText: "Alice prefers coffee.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, ws := buildAgentHandlers(t)

			res := run(t, h["memory_create"], newCall(tt.writer, map[string]any{
				"type": "fact", "text": tt.memoryText,
			}))
			if res.IsError {
				t.Fatalf("memory_create failed: %s", res.ForLLM)
			}

			if !fileExists(store.DBPath(tt.wantDir(ws))) {
				t.Fatalf("expected the memory store at %s", store.DBPath(tt.wantDir(ws)))
			}
			if fileExists(store.DBPath(tt.absentDir(ws))) {
				t.Fatalf("the write must not create a store at %s", store.DBPath(tt.absentDir(ws)))
			}

			found := run(t, h["memory_search"], newCall(tt.writer, map[string]any{"query": tt.memoryText}))
			if found.IsError || strings.Contains(found.ForLLM, "No active memories match") ||
				!strings.Contains(found.ForLLM, tt.memoryText) {
				t.Fatalf("writer's session should find the memory: %s", found.ForLLM)
			}
			other := run(t, h["memory_search"], newCall(tt.other, map[string]any{"query": tt.memoryText}))
			if other.IsError {
				t.Fatalf("memory_search in the other session failed: %s", other.ForLLM)
			}
			if !strings.Contains(other.ForLLM, "No active memories match") {
				t.Fatalf("the other session must not see the memory: %s", other.ForLLM)
			}
		})
	}
}

// TestSubagentConsolidateDoesNotTriggerPrimary checks that a sub-agent cannot
// start a consolidation run, which would work on the agent's own memory.
func TestSubagentConsolidateDoesNotTriggerPrimary(t *testing.T) {
	var triggered []string
	SetConsolidateTrigger(func(_, sessionKey string) { triggered = append(triggered, sessionKey) })
	t.Cleanup(func() { SetConsolidateTrigger(nil) })

	h, _ := buildAgentHandlers(t)

	run(t, h["consolidate"], newCall(subagentSession, nil))
	if len(triggered) != 0 {
		t.Fatalf("a sub-agent session must not trigger consolidation, got %v", triggered)
	}

	run(t, h["consolidate"], newCall(mainSession, nil))
	if len(triggered) != 1 || triggered[0] != mainSession {
		t.Fatalf("a main session should trigger consolidation once, got %v", triggered)
	}
}
