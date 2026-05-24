package tools

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"strings"
)

// EditFileTool edits a file by replacing old_text with new_text.
// The old_text must exist exactly in the file.
type EditFileTool struct {
	fs fileSystem
}

// NewEditFileTool creates a new EditFileTool with optional directory restriction.
func NewEditFileTool(workspace string, restrict bool, allowPaths ...[]*regexp.Regexp) *EditFileTool {
	var patterns []*regexp.Regexp
	if len(allowPaths) > 0 {
		patterns = allowPaths[0]
	}
	return &EditFileTool{fs: buildFs(workspace, restrict, patterns)}
}

// NewEditFileToolWithMemoryRedirect mirrors NewEditFileTool with redirect support.
func NewEditFileToolWithMemoryRedirect(
	workspace string,
	restrict bool,
	patterns []*regexp.Regexp,
	memoryRoot string,
) *EditFileTool {
	return &EditFileTool{fs: buildFsWithMemoryRedirect(workspace, restrict, patterns, memoryRoot)}
}

func (t *EditFileTool) Name() string {
	return "edit_file"
}

func (t *EditFileTool) Description() string {
	return "Edit a file by replacing old_text with new_text. The old_text must exist exactly in the file."
}

func (t *EditFileTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "The file path to edit",
			},
			"old_text": map[string]any{
				"type":        "string",
				"description": "The exact text to find and replace",
			},
			"new_text": map[string]any{
				"type":        "string",
				"description": "The text to replace with",
			},
			"display": map[string]any{
				"type":        "boolean",
				"description": "If true, after the operation, send the written/edited/appended content to the user as a fenced block separated by `---` markers.",
				"default":     false,
			},
			"backup": map[string]any{
				"type":        "boolean",
				"description": "If true and the target file exists, copy it to <file>.NNNN (next unused 4-digit suffix, starting at 0001) before modification. Silently skipped when the target does not exist.",
				"default":     false,
			},
		},
		"required": []string{"path", "old_text", "new_text"},
	}
}

func (t *EditFileTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	path, ok := args["path"].(string)
	if !ok {
		return ErrorResult("path is required")
	}

	oldText, ok := args["old_text"].(string)
	if !ok {
		return ErrorResult("old_text is required")
	}

	newText, ok := args["new_text"].(string)
	if !ok {
		return ErrorResult("new_text is required")
	}

	content, err := t.fs.ReadFile(path)
	if err != nil {
		return ErrorResult(err.Error())
	}
	newContent, err := replaceEditContent(content, oldText, newText)
	if err != nil {
		return ErrorResult(err.Error())
	}

	if getBoolArg(args, "backup", false) {
		if _, err := backupExistingFile(t.fs, path); err != nil {
			return ErrorResult(err.Error())
		}
	}

	if err := t.fs.WriteFile(path, newContent); err != nil {
		return ErrorResult(err.Error())
	}
	forLLM := fmt.Sprintf("File edited: %s", path)
	if getBoolArg(args, "display", false) {
		return &ToolResult{
			ForLLM:  forLLM,
			ForUser: displayBody(newText),
		}
	}
	return SilentResult(forLLM)
}

type AppendFileTool struct {
	fs fileSystem
}

func NewAppendFileTool(workspace string, restrict bool, allowPaths ...[]*regexp.Regexp) *AppendFileTool {
	var patterns []*regexp.Regexp
	if len(allowPaths) > 0 {
		patterns = allowPaths[0]
	}
	return &AppendFileTool{fs: buildFs(workspace, restrict, patterns)}
}

// NewAppendFileToolWithMemoryRedirect mirrors NewAppendFileTool with redirect support.
func NewAppendFileToolWithMemoryRedirect(
	workspace string,
	restrict bool,
	patterns []*regexp.Regexp,
	memoryRoot string,
) *AppendFileTool {
	return &AppendFileTool{fs: buildFsWithMemoryRedirect(workspace, restrict, patterns, memoryRoot)}
}

func (t *AppendFileTool) Name() string {
	return "append_file"
}

func (t *AppendFileTool) Description() string {
	return "Append content to the end of a file"
}

func (t *AppendFileTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "The file path to append to",
			},
			"content": map[string]any{
				"type":        "string",
				"description": "The content to append",
			},
			"display": map[string]any{
				"type":        "boolean",
				"description": "If true, after the operation, send the written/edited/appended content to the user as a fenced block separated by `---` markers.",
				"default":     false,
			},
			"backup": map[string]any{
				"type":        "boolean",
				"description": "If true and the target file exists, copy it to <file>.NNNN (next unused 4-digit suffix, starting at 0001) before modification. Silently skipped when the target does not exist.",
				"default":     false,
			},
		},
		"required": []string{"path", "content"},
	}
}

func (t *AppendFileTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	path, ok := args["path"].(string)
	if !ok {
		return ErrorResult("path is required")
	}

	content, ok := args["content"].(string)
	if !ok {
		return ErrorResult("content is required")
	}

	if getBoolArg(args, "backup", false) {
		if _, err := backupExistingFile(t.fs, path); err != nil {
			return ErrorResult(err.Error())
		}
	}

	if err := appendFile(t.fs, path, content); err != nil {
		return ErrorResult(err.Error())
	}
	forLLM := fmt.Sprintf("Appended to %s", path)
	if getBoolArg(args, "display", false) {
		return &ToolResult{
			ForLLM:  forLLM,
			ForUser: displayBody(content),
		}
	}
	return SilentResult(forLLM)
}

// appendFile reads the existing content (if any) via sysFs, appends new content, and writes back.
func appendFile(sysFs fileSystem, path, appendContent string) error {
	content, err := sysFs.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	newContent := append(content, []byte(appendContent)...)
	return sysFs.WriteFile(path, newContent)
}

// replaceEditContent handles the core logic of finding and replacing a single occurrence of oldText.
func replaceEditContent(content []byte, oldText, newText string) ([]byte, error) {
	contentStr := string(content)

	if !strings.Contains(contentStr, oldText) {
		return nil, fmt.Errorf("old_text not found in file. Make sure it matches exactly")
	}

	count := strings.Count(contentStr, oldText)
	if count > 1 {
		return nil, fmt.Errorf("old_text appears %d times. Please provide more context to make it unique", count)
	}

	newContent := strings.Replace(contentStr, oldText, newText, 1)
	return []byte(newContent), nil
}
