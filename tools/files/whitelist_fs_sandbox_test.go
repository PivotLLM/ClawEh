package files

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
