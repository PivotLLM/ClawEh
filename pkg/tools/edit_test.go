package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestEditTool_EditFile_Success verifies successful file editing
func TestEditTool_EditFile_Success(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")
	os.WriteFile(testFile, []byte("Hello World\nThis is a test"), 0o644)

	tool := NewEditFileTool(tmpDir, true)
	ctx := context.Background()
	args := map[string]any{
		"path":     testFile,
		"old_text": "World",
		"new_text": "Universe",
	}

	result := tool.Execute(ctx, args)

	// Success should not be an error
	if result.IsError {
		t.Errorf("Expected success, got IsError=true: %s", result.ForLLM)
	}

	// Should return SilentResult
	if !result.Silent {
		t.Errorf("Expected Silent=true for EditFile, got false")
	}

	// ForUser should be empty (silent result)
	if result.ForUser != "" {
		t.Errorf("Expected ForUser to be empty for SilentResult, got: %s", result.ForUser)
	}

	// Verify file was actually edited
	content, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("Failed to read edited file: %v", err)
	}
	contentStr := string(content)
	if !strings.Contains(contentStr, "Hello Universe") {
		t.Errorf("Expected file to contain 'Hello Universe', got: %s", contentStr)
	}
	if strings.Contains(contentStr, "Hello World") {
		t.Errorf("Expected 'Hello World' to be replaced, got: %s", contentStr)
	}
}

// TestEditTool_EditFile_NotFound verifies error handling for non-existent file
func TestEditTool_EditFile_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "nonexistent.txt")

	tool := NewEditFileTool(tmpDir, true)
	ctx := context.Background()
	args := map[string]any{
		"path":     testFile,
		"old_text": "old",
		"new_text": "new",
	}

	result := tool.Execute(ctx, args)

	// Should return error result
	if !result.IsError {
		t.Errorf("Expected error for non-existent file")
	}

	// Should mention file not found
	if !strings.Contains(result.ForLLM, "not found") && !strings.Contains(result.ForUser, "not found") {
		t.Errorf("Expected 'file not found' message, got ForLLM: %s", result.ForLLM)
	}
}

// TestEditTool_EditFile_OldTextNotFound verifies error when old_text doesn't exist
func TestEditTool_EditFile_OldTextNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")
	os.WriteFile(testFile, []byte("Hello World"), 0o644)

	tool := NewEditFileTool(tmpDir, true)
	ctx := context.Background()
	args := map[string]any{
		"path":     testFile,
		"old_text": "Goodbye",
		"new_text": "Hello",
	}

	result := tool.Execute(ctx, args)

	// Should return error result
	if !result.IsError {
		t.Errorf("Expected error when old_text not found")
	}

	// Should mention old_text not found
	if !strings.Contains(result.ForLLM, "not found") && !strings.Contains(result.ForUser, "not found") {
		t.Errorf("Expected 'not found' message, got ForLLM: %s", result.ForLLM)
	}
}

// TestEditTool_EditFile_MultipleMatches verifies error when old_text appears multiple times
func TestEditTool_EditFile_MultipleMatches(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")
	os.WriteFile(testFile, []byte("test test test"), 0o644)

	tool := NewEditFileTool(tmpDir, true)
	ctx := context.Background()
	args := map[string]any{
		"path":     testFile,
		"old_text": "test",
		"new_text": "done",
	}

	result := tool.Execute(ctx, args)

	// Should return error result
	if !result.IsError {
		t.Errorf("Expected error when old_text appears multiple times")
	}

	// Should mention multiple occurrences
	if !strings.Contains(result.ForLLM, "times") && !strings.Contains(result.ForUser, "times") {
		t.Errorf("Expected 'multiple times' message, got ForLLM: %s", result.ForLLM)
	}
}

// TestEditTool_EditFile_OutsideAllowedDir verifies error when path is outside allowed directory
func TestEditTool_EditFile_OutsideAllowedDir(t *testing.T) {
	tmpDir := t.TempDir()
	otherDir := t.TempDir()
	testFile := filepath.Join(otherDir, "test.txt")
	os.WriteFile(testFile, []byte("content"), 0o644)

	tool := NewEditFileTool(tmpDir, true) // Restrict to tmpDir
	ctx := context.Background()
	args := map[string]any{
		"path":     testFile,
		"old_text": "content",
		"new_text": "new",
	}

	result := tool.Execute(ctx, args)

	// Should return error result
	assert.True(t, result.IsError, "Expected error when path is outside allowed directory")

	// Should mention outside allowed directory
	// Note: ErrorResult only sets ForLLM by default, so ForUser might be empty.
	// We check ForLLM as it's the primary error channel.
	assert.True(
		t,
		strings.Contains(result.ForLLM, "outside") || strings.Contains(result.ForLLM, "access denied") ||
			strings.Contains(result.ForLLM, "escapes"),
		"Expected 'outside allowed' or 'access denied' message, got ForLLM: %s",
		result.ForLLM,
	)
}

// TestEditTool_EditFile_MissingPath verifies error handling for missing path
func TestEditTool_EditFile_MissingPath(t *testing.T) {
	tool := NewEditFileTool("", false)
	ctx := context.Background()
	args := map[string]any{
		"old_text": "old",
		"new_text": "new",
	}

	result := tool.Execute(ctx, args)

	// Should return error result
	if !result.IsError {
		t.Errorf("Expected error when path is missing")
	}
}

// TestEditTool_EditFile_MissingOldText verifies error handling for missing old_text
func TestEditTool_EditFile_MissingOldText(t *testing.T) {
	tool := NewEditFileTool("", false)
	ctx := context.Background()
	args := map[string]any{
		"path":     "/tmp/test.txt",
		"new_text": "new",
	}

	result := tool.Execute(ctx, args)

	// Should return error result
	if !result.IsError {
		t.Errorf("Expected error when old_text is missing")
	}
}

// TestEditTool_EditFile_MissingNewText verifies error handling for missing new_text
func TestEditTool_EditFile_MissingNewText(t *testing.T) {
	tool := NewEditFileTool("", false)
	ctx := context.Background()
	args := map[string]any{
		"path":     "/tmp/test.txt",
		"old_text": "old",
	}

	result := tool.Execute(ctx, args)

	// Should return error result
	if !result.IsError {
		t.Errorf("Expected error when new_text is missing")
	}
}

// TestEditTool_AppendFile_Success verifies successful file appending
func TestEditTool_AppendFile_Success(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")
	os.WriteFile(testFile, []byte("Initial content"), 0o644)

	tool := NewAppendFileTool("", false)
	ctx := context.Background()
	args := map[string]any{
		"path":    testFile,
		"content": "\nAppended content",
	}

	result := tool.Execute(ctx, args)

	// Success should not be an error
	if result.IsError {
		t.Errorf("Expected success, got IsError=true: %s", result.ForLLM)
	}

	// Should return SilentResult
	if !result.Silent {
		t.Errorf("Expected Silent=true for AppendFile, got false")
	}

	// ForUser should be empty (silent result)
	if result.ForUser != "" {
		t.Errorf("Expected ForUser to be empty for SilentResult, got: %s", result.ForUser)
	}

	// Verify content was actually appended
	content, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("Failed to read file: %v", err)
	}
	contentStr := string(content)
	if !strings.Contains(contentStr, "Initial content") {
		t.Errorf("Expected original content to remain, got: %s", contentStr)
	}
	if !strings.Contains(contentStr, "Appended content") {
		t.Errorf("Expected appended content, got: %s", contentStr)
	}
}

// TestEditTool_AppendFile_MissingPath verifies error handling for missing path
func TestEditTool_AppendFile_MissingPath(t *testing.T) {
	tool := NewAppendFileTool("", false)
	ctx := context.Background()
	args := map[string]any{
		"content": "test",
	}

	result := tool.Execute(ctx, args)

	// Should return error result
	if !result.IsError {
		t.Errorf("Expected error when path is missing")
	}
}

// TestEditTool_AppendFile_MissingContent verifies error handling for missing content
func TestEditTool_AppendFile_MissingContent(t *testing.T) {
	tool := NewAppendFileTool("", false)
	ctx := context.Background()
	args := map[string]any{
		"path": "/tmp/test.txt",
	}

	result := tool.Execute(ctx, args)

	// Should return error result
	if !result.IsError {
		t.Errorf("Expected error when content is missing")
	}
}

// TestReplaceEditContent verifies the helper function replaceEditContent
func TestReplaceEditContent(t *testing.T) {
	tests := []struct {
		name        string
		content     []byte
		oldText     string
		newText     string
		expected    []byte
		expectError bool
	}{
		{
			name:        "successful replacement",
			content:     []byte("hello world"),
			oldText:     "world",
			newText:     "universe",
			expected:    []byte("hello universe"),
			expectError: false,
		},
		{
			name:        "old text not found",
			content:     []byte("hello world"),
			oldText:     "golang",
			newText:     "rust",
			expected:    nil,
			expectError: true,
		},
		{
			name:        "multiple matches found",
			content:     []byte("test text test"),
			oldText:     "test",
			newText:     "done",
			expected:    nil,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := replaceEditContent(tt.content, tt.oldText, tt.newText)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

// TestAppendFileTool_AppendToNonExistent_Restricted verifies that AppendFileTool in restricted mode
// can append to a file that does not yet exist — it should silently create the file.
// This exercises the errors.Is(err, fs.ErrNotExist) path in appendFileWithRW + rootRW.
func TestAppendFileTool_AppendToNonExistent_Restricted(t *testing.T) {
	workspace := t.TempDir()
	tool := NewAppendFileTool(workspace, true)
	ctx := context.Background()

	args := map[string]any{
		"path":    "brand_new_file.txt",
		"content": "first content",
	}

	result := tool.Execute(ctx, args)
	assert.False(
		t,
		result.IsError,
		"Expected success when appending to non-existent file in restricted mode, got: %s",
		result.ForLLM,
	)

	// Verify the file was created with correct content
	data, err := os.ReadFile(filepath.Join(workspace, "brand_new_file.txt"))
	assert.NoError(t, err)
	assert.Equal(t, "first content", string(data))
}

// TestAppendFileTool_Restricted_Success verifies that AppendFileTool in restricted mode
// correctly appends to an existing file within the sandbox.
func TestAppendFileTool_Restricted_Success(t *testing.T) {
	workspace := t.TempDir()
	testFile := "existing.txt"
	err := os.WriteFile(filepath.Join(workspace, testFile), []byte("initial"), 0o644)
	assert.NoError(t, err)

	tool := NewAppendFileTool(workspace, true)
	ctx := context.Background()
	args := map[string]any{
		"path":    testFile,
		"content": " appended",
	}

	result := tool.Execute(ctx, args)
	assert.False(t, result.IsError, "Expected success, got: %s", result.ForLLM)
	assert.True(t, result.Silent)

	data, err := os.ReadFile(filepath.Join(workspace, testFile))
	assert.NoError(t, err)
	assert.Equal(t, "initial appended", string(data))
}

// TestEditFileTool_Restricted_InPlaceEdit verifies that EditFileTool in restricted mode
// correctly edits a file using the single-open editFileInRoot path.
func TestEditFileTool_Restricted_InPlaceEdit(t *testing.T) {
	workspace := t.TempDir()
	testFile := "edit_target.txt"
	err := os.WriteFile(filepath.Join(workspace, testFile), []byte("Hello World"), 0o644)
	assert.NoError(t, err)

	tool := NewEditFileTool(workspace, true)
	ctx := context.Background()
	args := map[string]any{
		"path":     testFile,
		"old_text": "World",
		"new_text": "Go",
	}

	result := tool.Execute(ctx, args)
	assert.False(t, result.IsError, "Expected success, got: %s", result.ForLLM)
	assert.True(t, result.Silent)

	data, err := os.ReadFile(filepath.Join(workspace, testFile))
	assert.NoError(t, err)
	assert.Equal(t, "Hello Go", string(data))
}

// TestEditTool_EditFile_Display_True verifies that when display=true on a successful
// edit, ForUser contains only new_text (not a diff, not the full updated file) wrapped
// in `---` markers.
func TestEditTool_EditFile_Display_True(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "edit_display.txt")
	original := "before MARKER after"
	err := os.WriteFile(testFile, []byte(original), 0o644)
	assert.NoError(t, err)

	tool := NewEditFileTool(tmpDir, true)
	newText := "REPLACED"
	args := map[string]any{
		"path":     testFile,
		"old_text": "MARKER",
		"new_text": newText,
		"display":  true,
	}

	result := tool.Execute(context.Background(), args)

	assert.False(t, result.IsError, "Expected success, got: %s", result.ForLLM)
	assert.False(t, result.Silent, "Expected Silent=false when display=true")
	assert.Equal(t, "---\nEdited: "+testFile+"\n\n"+newText+"\n---", result.ForUser)
	assert.Contains(t, result.ForLLM, "File edited:")

	// ForUser must NOT contain the unchanged surrounding content nor a diff.
	assert.NotContains(t, result.ForUser, "before")
	assert.NotContains(t, result.ForUser, "after")
}

// TestEditTool_EditFile_Display_False verifies display=false matches today's behavior.
func TestEditTool_EditFile_Display_False(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "edit_silent.txt")
	err := os.WriteFile(testFile, []byte("hello world"), 0o644)
	assert.NoError(t, err)

	tool := NewEditFileTool(tmpDir, true)
	result := tool.Execute(context.Background(), map[string]any{
		"path":     testFile,
		"old_text": "world",
		"new_text": "go",
		"display":  false,
	})

	assert.False(t, result.IsError)
	assert.True(t, result.Silent)
	assert.Equal(t, "", result.ForUser)
}

// TestEditTool_EditFile_Display_Absent verifies omitting display matches today's behavior.
func TestEditTool_EditFile_Display_Absent(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "edit_absent.txt")
	err := os.WriteFile(testFile, []byte("hello world"), 0o644)
	assert.NoError(t, err)

	tool := NewEditFileTool(tmpDir, true)
	result := tool.Execute(context.Background(), map[string]any{
		"path":     testFile,
		"old_text": "world",
		"new_text": "go",
	})

	assert.False(t, result.IsError)
	assert.True(t, result.Silent)
	assert.Equal(t, "", result.ForUser)
}

// TestEditTool_EditFile_Display_TrueOnFailure verifies that a failed edit with display=true
// does NOT emit ForUser.
func TestEditTool_EditFile_Display_TrueOnFailure(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "edit_fail.txt")
	err := os.WriteFile(testFile, []byte("hello world"), 0o644)
	assert.NoError(t, err)

	tool := NewEditFileTool(tmpDir, true)
	result := tool.Execute(context.Background(), map[string]any{
		"path":     testFile,
		"old_text": "MISSING",
		"new_text": "should not display",
		"display":  true,
	})

	assert.True(t, result.IsError, "Expected error when old_text not found")
	assert.Equal(t, "", result.ForUser, "Failed edits must never emit ForUser")
}

// TestEditTool_AppendFile_Display_True verifies that when display=true on a successful
// append, ForUser contains only the appended bytes (not the prior file content).
func TestEditTool_AppendFile_Display_True(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "append_display.txt")
	prior := "PRIOR_CONTENT"
	err := os.WriteFile(testFile, []byte(prior), 0o644)
	assert.NoError(t, err)

	tool := NewAppendFileTool("", false)
	appended := "ADDED_BYTES"
	result := tool.Execute(context.Background(), map[string]any{
		"path":    testFile,
		"content": appended,
		"display": true,
	})

	assert.False(t, result.IsError, "Expected success, got: %s", result.ForLLM)
	assert.False(t, result.Silent, "Expected Silent=false when display=true")
	assert.Equal(t, "---\nAppended: "+testFile+"\n\n"+appended+"\n---", result.ForUser)
	assert.Contains(t, result.ForLLM, "Appended to")

	// ForUser must NOT contain the prior file content.
	assert.NotContains(t, result.ForUser, prior)

	// File on disk should have both prior + appended.
	got, err := os.ReadFile(testFile)
	assert.NoError(t, err)
	assert.Equal(t, prior+appended, string(got))
}

// TestEditTool_AppendFile_Display_False verifies display=false matches today's behavior.
func TestEditTool_AppendFile_Display_False(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "append_silent.txt")
	err := os.WriteFile(testFile, []byte("x"), 0o644)
	assert.NoError(t, err)

	tool := NewAppendFileTool("", false)
	result := tool.Execute(context.Background(), map[string]any{
		"path":    testFile,
		"content": "y",
		"display": false,
	})

	assert.False(t, result.IsError)
	assert.True(t, result.Silent)
	assert.Equal(t, "", result.ForUser)
}

// TestEditTool_AppendFile_Display_Absent verifies omitting display matches today's behavior.
func TestEditTool_AppendFile_Display_Absent(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "append_absent.txt")
	err := os.WriteFile(testFile, []byte("x"), 0o644)
	assert.NoError(t, err)

	tool := NewAppendFileTool("", false)
	result := tool.Execute(context.Background(), map[string]any{
		"path":    testFile,
		"content": "y",
	})

	assert.False(t, result.IsError)
	assert.True(t, result.Silent)
	assert.Equal(t, "", result.ForUser)
}

// TestEditTool_AppendFile_Display_TrueOnFailure verifies a failed append with display=true
// does NOT emit ForUser.
func TestEditTool_AppendFile_Display_TrueOnFailure(t *testing.T) {
	workspace := t.TempDir()
	tool := NewAppendFileTool(workspace, true)

	result := tool.Execute(context.Background(), map[string]any{
		"path":    "../escape.txt",
		"content": "should never display",
		"display": true,
	})

	assert.True(t, result.IsError, "Expected error for path escaping workspace")
	assert.Equal(t, "", result.ForUser, "Failed appends must never emit ForUser")
}

// TestEditFileTool_Restricted_FileNotFound verifies that editFileInRoot returns a proper
// error message when the target file does not exist.
func TestEditFileTool_Restricted_FileNotFound(t *testing.T) {
	workspace := t.TempDir()
	tool := NewEditFileTool(workspace, true)
	ctx := context.Background()
	args := map[string]any{
		"path":     "no_such_file.txt",
		"old_text": "old",
		"new_text": "new",
	}

	result := tool.Execute(ctx, args)
	assert.True(t, result.IsError)
	assert.Contains(t, result.ForLLM, "not found")
}
