package tools

import (
	"testing"
)

// Tests for SpawnTool edge cases.

func TestSpawnTool_Execute_NilManagerExtra(t *testing.T) {
	// Create a SpawnTool with nil manager.
	tool := &SpawnTool{manager: nil}

	result := tool.Execute(t.Context(), map[string]any{
		"task": "do something",
	})
	if !result.IsError {
		t.Error("Execute() should error when manager is nil")
	}
}

func TestSpawnTool_Execute_SelfSpawnSkipsAllowlist(t *testing.T) {
	sm := NewSubagentManager(SubagentManagerConfig{})
	sm.callerAgentID = "parent-agent"
	tool := NewSpawnTool(sm)

	// Self-spawns (no agent_id) must not consult the allowlist at all —
	// authorization comes from the tool being registered in the agent's registry.
	checkerCalled := false
	tool.SetAllowlistChecker(func(targetAgentID string) bool {
		checkerCalled = true
		return false
	})

	// No "agent_id" — self-spawn path.
	tool.Execute(t.Context(), map[string]any{
		"task": "do something",
	})
	if checkerCalled {
		t.Error("allowlist checker must not be called for self-spawns (no agent_id)")
	}
}

func TestSpawnTool_Execute_AllowlistEmptyCheckID_Allowed(t *testing.T) {
	sm := NewSubagentManager(SubagentManagerConfig{})
	// callerAgentID is empty, so allowlist won't be invoked.
	sm.callerAgentID = ""
	tool := NewSpawnTool(sm)

	checkerCalled := false
	tool.SetAllowlistChecker(func(targetAgentID string) bool {
		checkerCalled = true
		return false
	})

	// With empty agentID and empty callerAgentID, checkID == "" → allowlist not invoked.
	// Manager.Spawn will be called and may fail since there's no provider configured.
	result := tool.Execute(t.Context(), map[string]any{
		"task":     "do something",
		"agent_id": "",
	})
	// The checker should NOT have been called (checkID is empty).
	if checkerCalled {
		t.Error("allowlist checker should not be called when checkID is empty")
	}
	// Result may error due to missing provider config — that's expected.
	_ = result
}
