package agents

import "testing"

// Tests for SpawnTool Name/Description/Parameters and SubagentManager metadata.

func TestSpawnTool_Metadata(t *testing.T) {
	sm := NewSubagentManager(SubagentManagerConfig{})
	tool := NewSpawnTool(sm)

	if tool.Name() != "agents_spawn" {
		t.Errorf("Name() = %q, want agents_spawn", tool.Name())
	}
	if tool.Description() == "" {
		t.Error("Description() should not be empty")
	}
	params := tool.Parameters()
	if params == nil {
		t.Error("Parameters() should not be nil")
	}
	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatal("Parameters().properties should be a map")
	}
	if _, ok := props["task"]; !ok {
		t.Error("Parameters() should include 'task'")
	}
}

func TestSpawnTool_SetAllowlistChecker(t *testing.T) {
	sm := NewSubagentManager(SubagentManagerConfig{})
	tool := NewSpawnTool(sm)

	called := false
	checker := func(targetAgentID string) bool {
		called = true
		return true
	}
	// Verify SetAllowlistChecker does not panic.
	tool.SetAllowlistChecker(checker)
	_ = called
}

func TestSubagentManager_GetTask_NotFound(t *testing.T) {
	sm := NewSubagentManager(SubagentManagerConfig{})
	_, ok := sm.GetTask("nonexistent")
	if ok {
		t.Error("GetTask() should return false for non-existent task")
	}
}

func TestSubagentManager_ListTasks_Empty(t *testing.T) {
	sm := NewSubagentManager(SubagentManagerConfig{})
	tasks := sm.ListTasks()
	if len(tasks) != 0 {
		t.Errorf("ListTasks() len = %d, want 0", len(tasks))
	}
}
