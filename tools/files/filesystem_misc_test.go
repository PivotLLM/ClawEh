package files

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/PivotLLM/ClawEh/utils"
)

// Tests for NewReadFileTool default maxSize and hostFs.Open permission error.

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
	if err := os.WriteFile(path, []byte("content"), 0o000); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chmod(path, 0o644); err != nil { // cleanup
			t.Error(err)
		}
	}()

	fs := &hostFs{}
	_, err := fs.Open(path)
	if err == nil {
		t.Error("Open() should error for permission-denied file")
	}
}

func TestSandboxFs_Open_ExistingFile(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "test.txt")
	if err := os.WriteFile(path, []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}

	fs := &sandboxFs{workspace: workspace}
	f, err := fs.Open(path)
	if err != nil {
		t.Fatalf("sandboxFs.Open() error = %v", err)
	}
	defer utils.CloseQuietly(f)
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
