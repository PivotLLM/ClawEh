package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestDisplayBody_WithHeader verifies the header line and blank separator are
// inserted before the payload inside the fenced block.
func TestDisplayBody_WithHeader(t *testing.T) {
	got := displayBody("Wrote: /tmp/x.txt", "payload")
	assert.Equal(t, "---\nWrote: /tmp/x.txt\n\npayload\n---", got)
}

// TestDisplayBody_EmptyHeader verifies that an empty header omits the header
// line entirely (no leading blank line, no trailing space).
func TestDisplayBody_EmptyHeader(t *testing.T) {
	got := displayBody("", "payload")
	assert.Equal(t, "---\npayload\n---", got)
}

// TestDisplayHeader_EmptyPath verifies displayHeader returns an empty string
// when path is empty, so callers omit the header line.
func TestDisplayHeader_EmptyPath(t *testing.T) {
	assert.Equal(t, "", displayHeader("Wrote", ""))
}

// TestDisplayHeader_NonEmpty verifies header construction for each verb.
func TestDisplayHeader_NonEmpty(t *testing.T) {
	assert.Equal(t, "Wrote: /a/b", displayHeader("Wrote", "/a/b"))
	assert.Equal(t, "Edited: /a/b", displayHeader("Edited", "/a/b"))
	assert.Equal(t, "Appended: /a/b", displayHeader("Appended", "/a/b"))
}

// TestWriteFileTool_Display_HeaderVerb verifies write_file uses the "Wrote:" verb.
func TestWriteFileTool_Display_HeaderVerb(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "verb.txt")

	tool := NewWriteFileTool("", false)
	result := tool.Execute(context.Background(), map[string]any{
		"path":    testFile,
		"content": "body",
		"display": true,
	})

	assert.False(t, result.IsError)
	assert.Equal(t, "---\nWrote: "+testFile+"\n\nbody\n---", result.ForUser)
}

// TestEditFileTool_Display_HeaderVerb verifies edit_file uses the "Edited:" verb.
func TestEditFileTool_Display_HeaderVerb(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "verb.txt")
	err := os.WriteFile(testFile, []byte("AAA"), 0o644)
	assert.NoError(t, err)

	tool := NewEditFileTool(tmpDir, true)
	result := tool.Execute(context.Background(), map[string]any{
		"path":     testFile,
		"old_text": "AAA",
		"new_text": "BBB",
		"display":  true,
	})

	assert.False(t, result.IsError)
	assert.Equal(t, "---\nEdited: "+testFile+"\n\nBBB\n---", result.ForUser)
}

// TestAppendFileTool_Display_HeaderVerb verifies append_file uses the "Appended:" verb.
func TestAppendFileTool_Display_HeaderVerb(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "verb.txt")
	err := os.WriteFile(testFile, []byte("prior"), 0o644)
	assert.NoError(t, err)

	tool := NewAppendFileTool("", false)
	result := tool.Execute(context.Background(), map[string]any{
		"path":    testFile,
		"content": "more",
		"display": true,
	})

	assert.False(t, result.IsError)
	assert.Equal(t, "---\nAppended: "+testFile+"\n\nmore\n---", result.ForUser)
}

// TestWriteFileTool_Display_EmptyPath verifies the defensive branch: when the
// LLM passes an empty path string, the header is omitted (no "Wrote: " with a
// trailing space).
func TestWriteFileTool_Display_EmptyPath(t *testing.T) {
	// Use an unrestricted workspace so the write attempts to land on disk; an
	// empty path will fail at the write step, but we exercise the header
	// helper directly to cover the defensive omission.
	got := displayBody(displayHeader("Wrote", ""), "body")
	assert.Equal(t, "---\nbody\n---", got)
	assert.NotContains(t, got, "Wrote: ")
}
