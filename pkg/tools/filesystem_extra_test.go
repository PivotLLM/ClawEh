package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Tests for ReadFileTool.Execute — covering offset/length argument branches.

func TestReadFileTool_Execute_WithOffset(t *testing.T) {
	tmpDir := t.TempDir()
	f := filepath.Join(tmpDir, "data.txt")
	content := "0123456789abcdef"
	if err := os.WriteFile(f, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewReadFileTool("", false, MaxReadFileSize)
	result := tool.Execute(context.Background(), map[string]any{
		"path":   f,
		"offset": float64(4),
	})

	if result.IsError {
		t.Fatalf("expected success, got: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "456789abcdef") {
		t.Errorf("result should contain file content from offset 4, got: %s", result.ForLLM)
	}
}

func TestReadFileTool_Execute_NegativeOffset(t *testing.T) {
	tmpDir := t.TempDir()
	f := filepath.Join(tmpDir, "data.txt")
	os.WriteFile(f, []byte("hello"), 0o644)

	tool := NewReadFileTool("", false, MaxReadFileSize)
	result := tool.Execute(context.Background(), map[string]any{
		"path":   f,
		"offset": float64(-1),
	})

	if !result.IsError {
		t.Fatal("expected error for negative offset")
	}
	if !strings.Contains(result.ForLLM, "offset must be >= 0") {
		t.Errorf("expected 'offset must be >= 0', got: %s", result.ForLLM)
	}
}

func TestReadFileTool_Execute_ZeroLength(t *testing.T) {
	tmpDir := t.TempDir()
	f := filepath.Join(tmpDir, "data.txt")
	os.WriteFile(f, []byte("hello"), 0o644)

	tool := NewReadFileTool("", false, MaxReadFileSize)
	result := tool.Execute(context.Background(), map[string]any{
		"path":   f,
		"length": float64(0),
	})

	if !result.IsError {
		t.Fatal("expected error for zero length")
	}
	if !strings.Contains(result.ForLLM, "length must be > 0") {
		t.Errorf("expected 'length must be > 0', got: %s", result.ForLLM)
	}
}

func TestReadFileTool_Execute_InvalidOffset(t *testing.T) {
	tmpDir := t.TempDir()
	f := filepath.Join(tmpDir, "data.txt")
	os.WriteFile(f, []byte("hello"), 0o644)

	tool := NewReadFileTool("", false, MaxReadFileSize)
	result := tool.Execute(context.Background(), map[string]any{
		"path":   f,
		"offset": "not-a-number",
	})

	if !result.IsError {
		t.Fatal("expected error for invalid offset type")
	}
}

func TestReadFileTool_Execute_InvalidLength(t *testing.T) {
	tmpDir := t.TempDir()
	f := filepath.Join(tmpDir, "data.txt")
	os.WriteFile(f, []byte("hello"), 0o644)

	tool := NewReadFileTool("", false, MaxReadFileSize)
	result := tool.Execute(context.Background(), map[string]any{
		"path":   f,
		"length": "bad",
	})

	if !result.IsError {
		t.Fatal("expected error for invalid length")
	}
}

func TestReadFileTool_Execute_LengthExceedsMaxCapped(t *testing.T) {
	tmpDir := t.TempDir()
	f := filepath.Join(tmpDir, "data.txt")
	os.WriteFile(f, []byte("hello world"), 0o644)

	tool := NewReadFileTool("", false, 5) // maxSize = 5
	result := tool.Execute(context.Background(), map[string]any{
		"path":   f,
		"length": float64(10000), // larger than max; should be capped
	})

	if result.IsError {
		t.Fatalf("expected success, got: %s", result.ForLLM)
	}
	// Content should be truncated to 5 bytes
	if !strings.Contains(result.ForLLM, "hello") {
		t.Errorf("expected 'hello' in result, got: %s", result.ForLLM)
	}
}

func TestReadFileTool_Execute_OffsetBeyondEnd(t *testing.T) {
	tmpDir := t.TempDir()
	f := filepath.Join(tmpDir, "data.txt")
	os.WriteFile(f, []byte("hi"), 0o644)

	tool := NewReadFileTool("", false, MaxReadFileSize)
	result := tool.Execute(context.Background(), map[string]any{
		"path":   f,
		"offset": float64(1000),
	})

	if result.IsError {
		t.Fatalf("expected no error for offset beyond end, got: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "END OF FILE") {
		t.Errorf("expected END OF FILE indicator, got: %s", result.ForLLM)
	}
}

// Tests for sandboxFs — write within sandbox.

func TestSandboxFs_WriteFile_Success(t *testing.T) {
	workspace := t.TempDir()
	tool := NewWriteFileTool(workspace, true)

	result := tool.Execute(context.Background(), map[string]any{
		"path":    "sandboxed.txt",
		"content": "sandboxed content",
	})

	if result.IsError {
		t.Fatalf("sandboxed write failed: %s", result.ForLLM)
	}

	data, err := os.ReadFile(filepath.Join(workspace, "sandboxed.txt"))
	if err != nil {
		t.Fatalf("could not read written file: %v", err)
	}
	if string(data) != "sandboxed content" {
		t.Errorf("content = %q, want 'sandboxed content'", string(data))
	}
}

func TestSandboxFs_WriteFile_Subdir(t *testing.T) {
	workspace := t.TempDir()
	tool := NewWriteFileTool(workspace, true)

	result := tool.Execute(context.Background(), map[string]any{
		"path":    "sub/dir/file.txt",
		"content": "nested content",
	})

	if result.IsError {
		t.Fatalf("sandboxed subdir write failed: %s", result.ForLLM)
	}

	data, err := os.ReadFile(filepath.Join(workspace, "sub", "dir", "file.txt"))
	if err != nil {
		t.Fatalf("could not read written file: %v", err)
	}
	if string(data) != "nested content" {
		t.Errorf("content = %q, want 'nested content'", string(data))
	}
}

func TestSandboxFs_ReadFile_Success(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "read.txt"), []byte("sandbox read"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewReadFileTool(workspace, true, MaxReadFileSize)
	result := tool.Execute(context.Background(), map[string]any{
		"path": "read.txt",
	})

	if result.IsError {
		t.Fatalf("sandboxed read failed: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "sandbox read") {
		t.Errorf("expected 'sandbox read' in result, got: %s", result.ForLLM)
	}
}

func TestSandboxFs_ReadFile_OutsideWorkspace(t *testing.T) {
	workspace := t.TempDir()
	tool := NewReadFileTool(workspace, true, MaxReadFileSize)

	result := tool.Execute(context.Background(), map[string]any{
		"path": "../outside.txt",
	})

	// Sandbox should prevent access outside the workspace
	if !result.IsError {
		t.Fatal("expected error when reading outside sandbox")
	}
}

// Tests for validatePath coverage.

func TestValidatePath_RelativePath(t *testing.T) {
	workspace := t.TempDir()
	path, err := validatePath("subdir/file.txt", workspace, true)
	if err != nil {
		t.Fatalf("validatePath() error = %v", err)
	}
	expected := filepath.Join(workspace, "subdir", "file.txt")
	if path != expected {
		t.Errorf("path = %q, want %q", path, expected)
	}
}

func TestValidatePath_AbsolutePathOutsideWorkspace(t *testing.T) {
	workspace := t.TempDir()
	_, err := validatePath("/etc/passwd", workspace, true)
	if err == nil {
		t.Fatal("expected error for path outside workspace")
	}
	if !strings.Contains(err.Error(), "access denied") {
		t.Errorf("error = %q, want 'access denied'", err.Error())
	}
}

func TestValidatePath_EmptyWorkspace(t *testing.T) {
	_, err := validatePath("file.txt", "", true)
	if err == nil {
		t.Fatal("expected error for empty workspace")
	}
	if !strings.Contains(err.Error(), "workspace is not defined") {
		t.Errorf("error = %q, want 'workspace is not defined'", err.Error())
	}
}

func TestValidatePath_UnrestrictedAllowsAnyPath(t *testing.T) {
	// When restrict=false, any path should be allowed
	path, err := validatePath("/tmp/anything.txt", t.TempDir(), false)
	if err != nil {
		t.Fatalf("validatePath() error = %v", err)
	}
	if path != "/tmp/anything.txt" {
		t.Errorf("path = %q, want /tmp/anything.txt", path)
	}
}

// Tests for ListDir tool.

func TestListDirTool_Success(t *testing.T) {
	workspace := t.TempDir()
	os.WriteFile(filepath.Join(workspace, "a.txt"), []byte("a"), 0o644)
	os.Mkdir(filepath.Join(workspace, "subdir"), 0o755)

	tool := NewListDirTool("", false)
	result := tool.Execute(context.Background(), map[string]any{
		"path": workspace,
	})

	if result.IsError {
		t.Fatalf("list dir failed: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "a.txt") {
		t.Errorf("expected 'a.txt' in result, got: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "subdir") {
		t.Errorf("expected 'subdir' in result, got: %s", result.ForLLM)
	}
}

func TestListDirTool_NonExistentDir(t *testing.T) {
	tool := NewListDirTool("", false)
	result := tool.Execute(context.Background(), map[string]any{
		"path": "/nonexistent/dir/xyz",
	})

	if !result.IsError {
		t.Fatal("expected error for non-existent directory")
	}
}
