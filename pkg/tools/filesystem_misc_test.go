package tools

import (
	"os"
	"path/filepath"
	"testing"
)

// Tests for resolveExistingAncestor, NewReadFileTool default maxSize, and hostFs.Open permission error.

func TestResolveExistingAncestor_ExistingPath(t *testing.T) {
	dir := t.TempDir()
	resolved, err := resolveExistingAncestor(dir)
	if err != nil {
		t.Fatalf("resolveExistingAncestor() error = %v", err)
	}
	if resolved == "" {
		t.Error("expected non-empty resolved path")
	}
}

func TestResolveExistingAncestor_NonExistentPath(t *testing.T) {
	dir := t.TempDir()
	// Create a path that doesn't exist within the temp dir.
	path := filepath.Join(dir, "nonexistent", "deep", "path")
	resolved, err := resolveExistingAncestor(path)
	if err != nil {
		t.Fatalf("resolveExistingAncestor() error = %v (expected to find ancestor)", err)
	}
	// Should have resolved to an ancestor that does exist (the temp dir).
	if resolved == "" {
		t.Error("expected non-empty resolved ancestor path")
	}
}

func TestResolveExistingAncestor_NonExistentRoot(t *testing.T) {
	// Path completely outside any real directory — will traverse up to root.
	// This tests the "Dir(current) == current" exit condition at root.
	_, err := resolveExistingAncestor("/nonexistent-root-level-xyz-abc-123")
	// May succeed (resolves to /) or fail; just ensure no panic.
	_ = err
}

func TestNewReadFileTool_DefaultMaxSize(t *testing.T) {
	// maxReadFileSize <= 0 should use MaxReadFileSize default.
	tool := NewReadFileTool("", false, 0)
	if tool.maxSize != MaxReadFileSize {
		t.Errorf("maxSize = %d, want %d (MaxReadFileSize)", tool.maxSize, MaxReadFileSize)
	}
}

func TestNewReadFileTool_NegativeMaxSize(t *testing.T) {
	tool := NewReadFileTool("", false, -1)
	if tool.maxSize != MaxReadFileSize {
		t.Errorf("maxSize = %d, want %d (MaxReadFileSize)", tool.maxSize, MaxReadFileSize)
	}
}

func TestHostFs_Open_PermissionDenied(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root, permission denial tests don't apply")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "noperm.txt")
	os.WriteFile(path, []byte("content"), 0o000)
	defer os.Chmod(path, 0o644) // cleanup

	fs := &hostFs{}
	_, err := fs.Open(path)
	if err == nil {
		t.Error("Open() should error for permission-denied file")
	}
}

func TestSandboxFs_Open_ExistingFile(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "test.txt")
	os.WriteFile(path, []byte("content"), 0o644)

	fs := &sandboxFs{workspace: workspace}
	f, err := fs.Open(path)
	if err != nil {
		t.Fatalf("sandboxFs.Open() error = %v", err)
	}
	defer f.Close()
}

func TestSandboxFs_Open_NonExistent(t *testing.T) {
	workspace := t.TempDir()
	fs := &sandboxFs{workspace: workspace}

	_, err := fs.Open(filepath.Join(workspace, "missing.txt"))
	if err == nil {
		t.Error("sandboxFs.Open() should error for non-existent file")
	}
}

func TestGetSafeRelPath_OutsideWorkspace(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()

	_, err := getSafeRelPath(workspace, filepath.Join(outside, "file.txt"))
	if err == nil {
		t.Error("getSafeRelPath() should error for path outside workspace")
	}
}

func TestGetSafeRelPath_RelativePath(t *testing.T) {
	workspace := t.TempDir()

	// Relative path should be resolved within workspace.
	rel, err := getSafeRelPath(workspace, "subdir/file.txt")
	if err != nil {
		t.Fatalf("getSafeRelPath() error = %v", err)
	}
	if rel == "" {
		t.Error("expected non-empty relative path")
	}
}

func TestGetSafeRelPath_EmptyWorkspace(t *testing.T) {
	_, err := getSafeRelPath("", "file.txt")
	if err == nil {
		t.Error("getSafeRelPath() should error for empty workspace")
	}
}
