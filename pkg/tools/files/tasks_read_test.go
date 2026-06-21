package files

import (
	"context"
	"path/filepath"
	"testing"
)

// TestReadScoped_TasksReadable verifies that including tasks/ in the read scope
// (the files provider always does this when a scope is active) makes the
// sub-agent results the spawn callback points at — tasks/<uuid>-results.json —
// readable, while other workspace-root files stay denied.
func TestReadScoped_TasksReadable(t *testing.T) {
	workspace := t.TempDir()
	mustWrite(t, filepath.Join(workspace, "tasks", "u1-results.json"), `{"content":"ok"}`)
	mustWrite(t, filepath.Join(workspace, "COGMEM.md"), "subsystem prompt — not for the agent")

	SetReadScopeSubdirs(appendIfMissing([]string{"files", "skills"}, "tasks"))
	defer SetReadScopeSubdirs(nil)

	read := NewReadFileTool(workspace, true, MaxReadFileSize)
	if r := read.Execute(context.Background(), map[string]any{"path": "tasks/u1-results.json"}); r.IsError {
		t.Fatalf("sub-agent results should be readable: %s", r.ForLLM)
	}
	if r := read.Execute(context.Background(), map[string]any{"path": "COGMEM.md"}); !r.IsError {
		t.Fatalf("non-scoped root file should still be denied")
	}
}

// TestAppendIfMissing covers the dedupe helper used to inject tasks/ into the scope.
func TestAppendIfMissing(t *testing.T) {
	got := appendIfMissing([]string{"files", "skills"}, "tasks")
	if len(got) != 3 || got[2] != "tasks" {
		t.Errorf("expected tasks appended, got %v", got)
	}
	if out := appendIfMissing([]string{"files", "tasks"}, "tasks"); len(out) != 2 {
		t.Errorf("expected no duplicate, got %v", out)
	}
}
