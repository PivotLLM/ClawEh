package tools

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Tests for whitelistFs non-matching paths (falls through to sandboxFs).

func TestWhitelistFs_WriteFile_NonMatchingPath_GoesToSandbox(t *testing.T) {
	workspace := t.TempDir()

	// Pattern matches nothing related to our test files.
	pattern := regexp.MustCompile(`^/no/match/here$`)
	tool := NewWriteFileTool(workspace, true, []*regexp.Regexp{pattern})

	targetFile := filepath.Join(workspace, "sandbox-write.txt")
	result := tool.Execute(t.Context(), map[string]any{
		"path":    targetFile,
		"content": "sandbox content",
	})
	if result.IsError {
		t.Fatalf("Execute() error: %s", result.ForLLM)
	}

	data, err := os.ReadFile(targetFile)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(data) != "sandbox content" {
		t.Errorf("content = %q, want 'sandbox content'", string(data))
	}
}

func TestWhitelistFs_ReadDir_NonMatchingPath_GoesToSandbox(t *testing.T) {
	workspace := t.TempDir()
	os.WriteFile(filepath.Join(workspace, "indir.txt"), []byte("x"), 0o644)

	pattern := regexp.MustCompile(`^/no/match/here$`)
	tool := NewListDirTool(workspace, true, []*regexp.Regexp{pattern})

	result := tool.Execute(t.Context(), map[string]any{
		"path": workspace,
	})
	if result.IsError {
		t.Fatalf("Execute() error: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "indir.txt") {
		t.Errorf("expected 'indir.txt' in result, got: %s", result.ForLLM)
	}
}

// Tests for subagent.SetTools and RegisterTool.
func TestSubagentManager_SetTools(t *testing.T) {
	sm := NewSubagentManager(SubagentManagerConfig{})
	registry := NewToolRegistry()
	sm.SetTools(registry)
	// Should not panic.
}

func TestSubagentManager_RegisterTool(t *testing.T) {
	sm := NewSubagentManager(SubagentManagerConfig{})
	sm.RegisterTool(&mockTool{name: "my-tool"})
	// Should not panic.
}

// Tests for send_file.SetMediaStore.
func TestSendFileTool_SetMediaStore(t *testing.T) {
	tool := NewSendFileTool(t.TempDir(), false, 0, nil)
	tool.SetMediaStore(nil)
	// SetMediaStore(nil) should not panic.
}

// Tests for spawn.ExecuteAsync.
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
