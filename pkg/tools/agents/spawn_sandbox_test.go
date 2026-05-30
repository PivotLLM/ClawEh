package agents

import (
	"context"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/tools"
)

// Tests for SubagentManager.SetTools and RegisterTool.
func TestSubagentManager_SetTools(t *testing.T) {
	sm := NewSubagentManager(SubagentManagerConfig{})
	registry := tools.NewToolRegistry()
	sm.SetTools(registry)
	// Should not panic.
}

func TestSubagentManager_RegisterTool(t *testing.T) {
	sm := NewSubagentManager(SubagentManagerConfig{})
	sm.RegisterTool(&mockAgentTool{name: "my-tool"})
	// Should not panic.
}

// Tests for SpawnTool.ExecuteAsync.
func TestSpawnTool_ExecuteAsync_MissingTask(t *testing.T) {
	sm := NewSubagentManager(SubagentManagerConfig{})
	tool := NewSpawnTool(sm)

	result := tool.ExecuteAsync(t.Context(), map[string]any{}, nil)
	if !result.IsError {
		t.Error("ExecuteAsync() should error when task is missing")
	}
}

func TestSpawnTool_ExecuteAsync_EmptyTask(t *testing.T) {
	sm := NewSubagentManager(SubagentManagerConfig{})
	tool := NewSpawnTool(sm)

	result := tool.ExecuteAsync(t.Context(), map[string]any{"task": "   "}, nil)
	if !result.IsError {
		t.Error("ExecuteAsync() should error for empty task")
	}
}

func TestSpawnTool_ExecuteAsync_AllowlistDeny(t *testing.T) {
	sm := NewSubagentManager(SubagentManagerConfig{})
	tool := NewSpawnTool(sm)

	// Set an allowlist that denies all agents.
	tool.SetAllowlistChecker(func(targetAgentID string) bool {
		return false // deny all
	})

	result := tool.ExecuteAsync(t.Context(), map[string]any{
		"task":     "do something",
		"agent_id": "blocked-agent",
	}, nil)
	if !result.IsError {
		t.Error("ExecuteAsync() should error when allowlist denies the agent")
	}
}

// mockAgentTool is a minimal Tool implementation for testing.
type mockAgentTool struct {
	name string
}

func (m *mockAgentTool) Name() string                                      { return m.name }
func (m *mockAgentTool) Description() string                               { return "mock " + m.name }
func (m *mockAgentTool) Parameters() map[string]any                        { return map[string]any{"type": "object"} }
func (m *mockAgentTool) Execute(_ context.Context, _ map[string]any) *tools.ToolResult {
	return tools.NewToolResult("ok")
}
