// Tests for Name(), Description(), Parameters() methods across tool types.
// These trivial 1-liners collectively affect coverage significantly.
package tools_test

import (
	"testing"

	toolsfiles "github.com/PivotLLM/ClawEh/pkg/tools/files"
)

func TestReadFileTool_Metadata(t *testing.T) {
	tool := toolsfiles.NewReadFileTool("", false, toolsfiles.MaxReadFileSize)
	if tool.Name() != "file_read" {
		t.Errorf("Name() = %q, want file_read", tool.Name())
	}
	if tool.Description() == "" {
		t.Error("Description() should not be empty")
	}
	params := tool.Parameters()
	if params == nil {
		t.Fatal("Parameters() should not be nil")
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
	tool := toolsfiles.NewWriteFileTool("", false)
	if tool.Name() != "file_write" {
		t.Errorf("Name() = %q, want file_write", tool.Name())
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
	tool := toolsfiles.NewListDirTool("", false)
	if tool.Name() != "file_list" {
		t.Errorf("Name() = %q, want file_list", tool.Name())
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
	tool := toolsfiles.NewEditFileTool("", false)
	if tool.Name() != "file_edit" {
		t.Errorf("Name() = %q, want file_edit", tool.Name())
	}
	if tool.Description() == "" {
		t.Error("Description() should not be empty")
	}
	params := tool.Parameters()
	if params == nil {
		t.Fatal("Parameters() should not be nil")
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
	tool := toolsfiles.NewAppendFileTool("", false)
	if tool.Name() != "file_append" {
		t.Errorf("Name() = %q, want file_append", tool.Name())
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
	tool := toolsfiles.NewAppendFileTool("", false)

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
	tool := toolsfiles.NewAppendFileTool("", false)
	result := tool.Execute(t.Context(), map[string]any{
		"content": "data",
	})
	if !result.IsError {
		t.Fatal("expected error for missing path")
	}
}

func TestAppendFileTool_Execute_MissingContent(t *testing.T) {
	tool := toolsfiles.NewAppendFileTool("", false)
	result := tool.Execute(t.Context(), map[string]any{
		"path": "/tmp/test.txt",
	})
	if !result.IsError {
		t.Fatal("expected error for missing content")
	}
}
