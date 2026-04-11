package tools

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestWhitelistFs_ReadFileViaWhitelist tests that whitelisted paths use hostFs.
func TestWhitelistFs_ReadFileViaWhitelist(t *testing.T) {
	workspace := t.TempDir()
	outsideDir := t.TempDir()

	outsideFile := filepath.Join(outsideDir, "outside.txt")
	if err := os.WriteFile(outsideFile, []byte("outside content"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Build a whitelist pattern matching the outside file.
	pattern := regexp.MustCompile(regexp.QuoteMeta(outsideFile))

	tool := NewReadFileTool(workspace, true, MaxReadFileSize, []*regexp.Regexp{pattern})
	result := tool.Execute(context.Background(), map[string]any{
		"path": outsideFile,
	})

	if result.IsError {
		t.Fatalf("whitelisted path read failed: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "outside content") {
		t.Errorf("expected 'outside content', got: %s", result.ForLLM)
	}
}

// TestWhitelistFs_WriteFileViaWhitelist tests that whitelisted paths use hostFs for write.
func TestWhitelistFs_WriteFileViaWhitelist(t *testing.T) {
	workspace := t.TempDir()
	outsideDir := t.TempDir()

	outsideFile := filepath.Join(outsideDir, "write-outside.txt")
	pattern := regexp.MustCompile(regexp.QuoteMeta(outsideDir))

	tool := NewWriteFileTool(workspace, true, []*regexp.Regexp{pattern})
	result := tool.Execute(context.Background(), map[string]any{
		"path":    outsideFile,
		"content": "whitelisted write",
	})

	if result.IsError {
		t.Fatalf("whitelisted path write failed: %s", result.ForLLM)
	}

	data, err := os.ReadFile(outsideFile)
	if err != nil {
		t.Fatalf("could not read written file: %v", err)
	}
	if string(data) != "whitelisted write" {
		t.Errorf("content = %q, want 'whitelisted write'", string(data))
	}
}

// TestWhitelistFs_ReadDirViaWhitelist tests that whitelisted dir listing uses hostFs.
func TestWhitelistFs_ReadDirViaWhitelist(t *testing.T) {
	workspace := t.TempDir()
	outsideDir := t.TempDir()

	// Create a file inside the outside dir.
	os.WriteFile(filepath.Join(outsideDir, "listed.txt"), []byte("x"), 0o644)

	pattern := regexp.MustCompile(regexp.QuoteMeta(outsideDir))
	tool := NewListDirTool(workspace, true, []*regexp.Regexp{pattern})
	result := tool.Execute(context.Background(), map[string]any{
		"path": outsideDir,
	})

	if result.IsError {
		t.Fatalf("whitelisted dir listing failed: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "listed.txt") {
		t.Errorf("expected 'listed.txt' in result, got: %s", result.ForLLM)
	}
}

// TestWhitelistFs_NonMatchingPathGoesToSandbox tests non-matching path uses sandbox.
func TestWhitelistFs_NonMatchingPathGoesToSandbox(t *testing.T) {
	workspace := t.TempDir()
	// Write a file inside workspace to read it successfully.
	inFile := filepath.Join(workspace, "inside.txt")
	os.WriteFile(inFile, []byte("inside content"), 0o644)

	// Whitelist matches nothing related to our test files.
	pattern := regexp.MustCompile(`^/no/match/here$`)
	tool := NewReadFileTool(workspace, true, MaxReadFileSize, []*regexp.Regexp{pattern})
	result := tool.Execute(context.Background(), map[string]any{
		"path": "inside.txt",
	})

	if result.IsError {
		t.Fatalf("sandbox path read failed: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "inside content") {
		t.Errorf("expected 'inside content', got: %s", result.ForLLM)
	}
}
