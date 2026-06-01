package files

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestDisplayBody_WithHeader verifies the header line is emitted verbatim
// (callers wrap the verb prefix in bold themselves via displayHeader) and a
// separator rule (same glyph as the outer fences) is inserted between the
// header and the payload inside the fenced block.
func TestDisplayBody_WithHeader(t *testing.T) {
	got := displayBody("**Wrote:** /tmp/x.txt", "payload")
	assert.Equal(t, "---\n**Wrote:** /tmp/x.txt\n---\n\npayload\n---", got)
}

// TestDisplayBody_EmptyHeader verifies that an empty header omits the header
// line AND the separator rule entirely — only the outer fences and payload
// remain. This preserves the existing copy_file no-header behaviour.
func TestDisplayBody_EmptyHeader(t *testing.T) {
	got := displayBody("", "payload")
	assert.Equal(t, "---\npayload\n---", got)
	assert.NotContains(t, got, "**")
}

// TestDisplayBody_HeaderShapeExact pins the exact non-empty header shape
// requested by the tool-ergonomics spec: outer fence, header line with the
// verb prefix bolded, separator rule using the same glyph as the fences,
// blank line, payload, closing fence.
func TestDisplayBody_HeaderShapeExact(t *testing.T) {
	got := displayBody(displayHeader("Wrote", "memory/notes.md"), "<payload>")
	want := "---\n**Wrote:** memory/notes.md\n---\n\n<payload>\n---"
	assert.Equal(t, want, got)
}

// TestDisplayBody_EmptyHeaderNoSeparator verifies the empty-header (copy_file)
// path emits neither a header line nor a separator rule between fences.
func TestDisplayBody_EmptyHeaderNoSeparator(t *testing.T) {
	got := displayBody("", "binary-or-copy-payload")
	// Exactly two `---` lines (the outer fences) — no extra rule.
	assert.Equal(t, 2, strings.Count(got, "---"))
	assert.Equal(t, "---\nbinary-or-copy-payload\n---", got)
}

// TestDisplayHeader_EmptyPath verifies displayHeader returns an empty string
// when path is empty, so callers omit the header line.
func TestDisplayHeader_EmptyPath(t *testing.T) {
	assert.Equal(t, "", displayHeader("Wrote", ""))
}

// TestDisplayHeader_NonEmpty verifies header construction for each verb.
// The verb prefix (verb + colon) is wrapped in bold markers; the path is
// left unbolded so a long path doesn't render as one giant bold run.
func TestDisplayHeader_NonEmpty(t *testing.T) {
	assert.Equal(t, "**Wrote:** /a/b", displayHeader("Wrote", "/a/b"))
	assert.Equal(t, "**Edited:** /a/b", displayHeader("Edited", "/a/b"))
	assert.Equal(t, "**Appended:** /a/b", displayHeader("Appended", "/a/b"))
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
	assert.Equal(t, "---\n**Wrote:** "+testFile+"\n---\n\nbody\n---", result.ForUser)
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
	assert.Equal(t, "---\n**Edited:** "+testFile+"\n---\n\nBBB\n---", result.ForUser)
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
	assert.Equal(t, "---\n**Appended:** "+testFile+"\n---\n\nmore\n---", result.ForUser)
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
