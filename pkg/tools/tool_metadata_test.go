// Tests for Name(), Description(), Parameters() methods across tool types.
// These trivial 1-liners collectively affect coverage significantly.
package tools

import (
	"testing"
)

func TestReadFileTool_Metadata(t *testing.T) {
	tool := NewReadFileTool("", false, MaxReadFileSize)
	if tool.Name() != "read_file" {
		t.Errorf("Name() = %q, want read_file", tool.Name())
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
	if _, ok := props["path"]; !ok {
		t.Error("Parameters() should include 'path'")
	}
}

func TestWriteFileTool_Metadata(t *testing.T) {
	tool := NewWriteFileTool("", false)
	if tool.Name() != "write_file" {
		t.Errorf("Name() = %q, want write_file", tool.Name())
	}
	if tool.Description() == "" {
		t.Error("Description() should not be empty")
	}
	params := tool.Parameters()
	if params == nil {
		t.Error("Parameters() should not be nil")
	}
}

func TestListDirTool_Metadata(t *testing.T) {
	tool := NewListDirTool("", false)
	if tool.Name() != "list_dir" {
		t.Errorf("Name() = %q, want list_dir", tool.Name())
	}
	if tool.Description() == "" {
		t.Error("Description() should not be empty")
	}
	params := tool.Parameters()
	if params == nil {
		t.Error("Parameters() should not be nil")
	}
}

func TestEditFileTool_Metadata(t *testing.T) {
	tool := NewEditFileTool("", false)
	if tool.Name() != "edit_file" {
		t.Errorf("Name() = %q, want edit_file", tool.Name())
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
	for _, required := range []string{"path", "old_text", "new_text"} {
		if _, ok := props[required]; !ok {
			t.Errorf("Parameters() missing %q", required)
		}
	}
}

func TestAppendFileTool_Metadata(t *testing.T) {
	tool := NewAppendFileTool("", false)
	if tool.Name() != "append_file" {
		t.Errorf("Name() = %q, want append_file", tool.Name())
	}
	if tool.Description() == "" {
		t.Error("Description() should not be empty")
	}
	params := tool.Parameters()
	if params == nil {
		t.Error("Parameters() should not be nil")
	}
}

func TestAppendFileTool_Execute_Success(t *testing.T) {
	dir := t.TempDir()
	tool := NewAppendFileTool("", false)

	// Append to a non-existent file — should create it.
	result := tool.Execute(t.Context(), map[string]any{
		"path":    dir + "/new.txt",
		"content": "appended content",
	})
	if result.IsError {
		t.Fatalf("append to new file failed: %s", result.ForLLM)
	}
}

func TestAppendFileTool_Execute_MissingPath(t *testing.T) {
	tool := NewAppendFileTool("", false)
	result := tool.Execute(t.Context(), map[string]any{
		"content": "data",
	})
	if !result.IsError {
		t.Fatal("expected error for missing path")
	}
}

func TestAppendFileTool_Execute_MissingContent(t *testing.T) {
	tool := NewAppendFileTool("", false)
	result := tool.Execute(t.Context(), map[string]any{
		"path": "/tmp/test.txt",
	})
	if !result.IsError {
		t.Fatal("expected error for missing content")
	}
}
