package files

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestWriteScoped_WriteInsideSubdirSucceeds verifies that a write to
// <workspace>/files/x.txt succeeds when the write subdir is "files".
func TestWriteScoped_WriteInsideSubdirSucceeds(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "files"), 0o755); err != nil {
		t.Fatal(err)
	}

	tool := NewWriteFileToolScoped(workspace, true, "files")
	result := tool.Execute(context.Background(), map[string]any{
		"path":    "files/x.txt",
		"content": "hello",
	})
	if result.IsError {
		t.Fatalf("write inside subdir failed: %s", result.ForLLM)
	}

	data, err := os.ReadFile(filepath.Join(workspace, "files", "x.txt"))
	if err != nil {
		t.Fatalf("could not read written file: %v", err)
	}
	if string(data) != "hello" {
		t.Errorf("content = %q, want 'hello'", string(data))
	}
}

// TestWriteScoped_WriteOutsideSubdirDenied verifies that a write to
// <workspace>/AGENTS.md is denied when writes are confined to the subdir.
func TestWriteScoped_WriteOutsideSubdirDenied(t *testing.T) {
	workspace := t.TempDir()

	tool := NewWriteFileToolScoped(workspace, true, "files")
	result := tool.Execute(context.Background(), map[string]any{
		"path":    "AGENTS.md",
		"content": "nope",
	})
	if !result.IsError {
		t.Fatalf("expected write outside subdir to be denied")
	}
	if !strings.Contains(result.ForLLM, "write denied") {
		t.Errorf("expected 'write denied' error, got: %s", result.ForLLM)
	}
	if _, err := os.Stat(filepath.Join(workspace, "AGENTS.md")); !os.IsNotExist(err) {
		t.Errorf("AGENTS.md should not have been created")
	}
}

// TestWriteScoped_ReadOutsideSubdirSucceeds verifies that reads anywhere in the
// workspace still succeed even though writes are confined to the subdir.
func TestWriteScoped_ReadOutsideSubdirSucceeds(t *testing.T) {
	workspace := t.TempDir()
	agentsFile := filepath.Join(workspace, "AGENTS.md")
	if err := os.WriteFile(agentsFile, []byte("agent instructions"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The edit tool reads then writes through the scoped fs; its read path must
	// still see the whole workspace.
	tool := NewEditFileToolScoped(workspace, true, "files")
	result := tool.Execute(context.Background(), map[string]any{
		"path":     "AGENTS.md",
		"old_text": "agent instructions",
		"new_text": "changed",
	})
	// The edit's read of AGENTS.md must succeed, but its write must be denied
	// because AGENTS.md is outside <workspace>/files.
	if !result.IsError {
		t.Fatalf("expected edit write outside subdir to be denied")
	}
	if !strings.Contains(result.ForLLM, "write denied") {
		t.Errorf("expected 'write denied' on edit write, got: %s", result.ForLLM)
	}

	// Confirm a plain read of the same path succeeds.
	reader := NewReadFileTool(workspace, true, MaxReadFileSize)
	rr := reader.Execute(context.Background(), map[string]any{"path": "AGENTS.md"})
	if rr.IsError {
		t.Fatalf("read of AGENTS.md failed: %s", rr.ForLLM)
	}
	if !strings.Contains(rr.ForLLM, "agent instructions") {
		t.Errorf("expected 'agent instructions', got: %s", rr.ForLLM)
	}
}

// TestWriteScoped_EmptySubdirAllowsWholeWorkspace verifies legacy behavior:
// when WorkspaceWriteSubdir is empty, writes anywhere in the workspace succeed.
func TestWriteScoped_EmptySubdirAllowsWholeWorkspace(t *testing.T) {
	workspace := t.TempDir()

	tool := NewWriteFileToolScoped(workspace, true, "")
	result := tool.Execute(context.Background(), map[string]any{
		"path":    "AGENTS.md",
		"content": "legacy write",
	})
	if result.IsError {
		t.Fatalf("legacy whole-workspace write failed: %s", result.ForLLM)
	}

	data, err := os.ReadFile(filepath.Join(workspace, "AGENTS.md"))
	if err != nil {
		t.Fatalf("could not read written file: %v", err)
	}
	if string(data) != "legacy write" {
		t.Errorf("content = %q, want 'legacy write'", string(data))
	}
}

// TestWriteScoped_AllowWritePathStillWritable verifies that a host path matching
// Tools.AllowWritePaths remains writable even when writes are confined to a subdir.
func TestWriteScoped_AllowWritePathStillWritable(t *testing.T) {
	workspace := t.TempDir()
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "allowed.txt")

	pattern := regexp.MustCompile(regexp.QuoteMeta(outsideDir))
	tool := NewWriteFileToolScoped(workspace, true, "files", []*regexp.Regexp{pattern})

	result := tool.Execute(context.Background(), map[string]any{
		"path":    outsideFile,
		"content": "whitelisted write",
	})
	if result.IsError {
		t.Fatalf("allow-write-path write failed: %s", result.ForLLM)
	}

	data, err := os.ReadFile(outsideFile)
	if err != nil {
		t.Fatalf("could not read written file: %v", err)
	}
	if string(data) != "whitelisted write" {
		t.Errorf("content = %q, want 'whitelisted write'", string(data))
	}
}
