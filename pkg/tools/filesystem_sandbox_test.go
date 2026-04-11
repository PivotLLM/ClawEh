package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Tests for sandboxFs.WriteFile, sandboxFs.ReadDir, hostFs.Open,
// and related paths not yet covered.

func TestSandboxFs_WriteFile_WithSubdir(t *testing.T) {
	workspace := t.TempDir()
	fs := &sandboxFs{workspace: workspace}

	path := filepath.Join(workspace, "subdir", "file.txt")
	err := fs.WriteFile(path, []byte("hello subdir"))
	if err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(workspace, "subdir", "file.txt"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(data) != "hello subdir" {
		t.Errorf("content = %q, want 'hello subdir'", string(data))
	}
}

func TestSandboxFs_WriteFile_RootPath(t *testing.T) {
	workspace := t.TempDir()
	fs := &sandboxFs{workspace: workspace}

	path := filepath.Join(workspace, "root-file.txt")
	err := fs.WriteFile(path, []byte("root content"))
	if err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(workspace, "root-file.txt"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(data) != "root content" {
		t.Errorf("content = %q, want 'root content'", string(data))
	}
}

func TestSandboxFs_WriteFile_EmptyWorkspace(t *testing.T) {
	fs := &sandboxFs{workspace: ""}
	err := fs.WriteFile("file.txt", []byte("data"))
	if err == nil {
		t.Error("WriteFile() should error with empty workspace")
	}
}

func TestSandboxFs_WriteFile_OutsideWorkspace(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	fs := &sandboxFs{workspace: workspace}

	path := filepath.Join(outside, "escape.txt")
	err := fs.WriteFile(path, []byte("data"))
	if err == nil {
		t.Error("WriteFile() should error for path outside workspace")
	}
}

func TestSandboxFs_ReadDir_Success(t *testing.T) {
	workspace := t.TempDir()
	os.WriteFile(filepath.Join(workspace, "a.txt"), []byte("a"), 0o644)
	os.WriteFile(filepath.Join(workspace, "b.txt"), []byte("b"), 0o644)

	fs := &sandboxFs{workspace: workspace}
	entries, err := fs.ReadDir(workspace)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if len(entries) < 2 {
		t.Errorf("ReadDir() len = %d, want >= 2", len(entries))
	}
}

func TestSandboxFs_ReadDir_NonExistentDir(t *testing.T) {
	workspace := t.TempDir()
	fs := &sandboxFs{workspace: workspace}

	_, err := fs.ReadDir(filepath.Join(workspace, "nonexistent"))
	if err == nil {
		t.Error("ReadDir() should error for non-existent directory")
	}
}

func TestSandboxFs_ReadDir_EmptyWorkspace(t *testing.T) {
	fs := &sandboxFs{workspace: ""}
	_, err := fs.ReadDir("some/path")
	if err == nil {
		t.Error("ReadDir() should error with empty workspace")
	}
}

func TestHostFs_Open_ExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("content"), 0o644)

	fs := &hostFs{}
	f, err := fs.Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer f.Close()
}

func TestHostFs_Open_NonExistent(t *testing.T) {
	fs := &hostFs{}
	_, err := fs.Open("/nonexistent/path/xyz.txt")
	if err == nil {
		t.Error("Open() should error for non-existent file")
	}
	if !strings.Contains(err.Error(), "file not found") {
		t.Errorf("error = %q, want 'file not found'", err.Error())
	}
}

func TestWriteFileTool_Execute_Success(t *testing.T) {
	dir := t.TempDir()
	tool := NewWriteFileTool(dir, true)

	result := tool.Execute(t.Context(), map[string]any{
		"path":    filepath.Join(dir, "output.txt"),
		"content": "test content",
	})
	if result.IsError {
		t.Fatalf("Execute() error: %s", result.ForLLM)
	}

	data, err := os.ReadFile(filepath.Join(dir, "output.txt"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(data) != "test content" {
		t.Errorf("content = %q, want 'test content'", string(data))
	}
}

func TestWriteFileTool_Execute_MissingPath(t *testing.T) {
	tool := NewWriteFileTool("", false)
	result := tool.Execute(t.Context(), map[string]any{
		"content": "data",
	})
	if !result.IsError {
		t.Error("Execute() should error for missing path")
	}
}

func TestWriteFileTool_Execute_MissingContent(t *testing.T) {
	tool := NewWriteFileTool("", false)
	result := tool.Execute(t.Context(), map[string]any{
		"path": "/tmp/test.txt",
	})
	if !result.IsError {
		t.Error("Execute() should error for missing content")
	}
}

func TestEditFileTool_Execute_Success(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "edit.txt")
	os.WriteFile(path, []byte("hello world"), 0o644)

	tool := NewEditFileTool(dir, true)
	result := tool.Execute(t.Context(), map[string]any{
		"path":     path,
		"old_text": "hello",
		"new_text": "goodbye",
	})
	if result.IsError {
		t.Fatalf("Execute() error: %s", result.ForLLM)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(data) != "goodbye world" {
		t.Errorf("content = %q, want 'goodbye world'", string(data))
	}
}

func TestEditFileTool_Execute_MissingPath(t *testing.T) {
	tool := NewEditFileTool("", false)
	result := tool.Execute(t.Context(), map[string]any{
		"old_text": "a",
		"new_text": "b",
	})
	if !result.IsError {
		t.Error("Execute() should error for missing path")
	}
}

func TestEditFileTool_Execute_MissingOldText(t *testing.T) {
	tool := NewEditFileTool("", false)
	result := tool.Execute(t.Context(), map[string]any{
		"path":     "/tmp/test.txt",
		"new_text": "b",
	})
	if !result.IsError {
		t.Error("Execute() should error for missing old_text")
	}
}

func TestEditFileTool_Execute_MissingNewText(t *testing.T) {
	tool := NewEditFileTool("", false)
	result := tool.Execute(t.Context(), map[string]any{
		"path":     "/tmp/test.txt",
		"old_text": "a",
	})
	if !result.IsError {
		t.Error("Execute() should error for missing new_text")
	}
}
