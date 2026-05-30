package files

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"

	"github.com/PivotLLM/ClawEh/pkg/tools"
)

type CopyFileTool struct {
	sysFs fileSystem
}

func NewCopyFileTool(workspace string, restrict bool, allowPaths ...[]*regexp.Regexp) *CopyFileTool {
	var patterns []*regexp.Regexp
	if len(allowPaths) > 0 {
		patterns = allowPaths[0]
	}
	return &CopyFileTool{sysFs: buildFs(workspace, restrict, patterns)}
}

func NewCopyFileToolWithMemoryRedirect(
	workspace string,
	restrict bool,
	patterns []*regexp.Regexp,
	memoryRoot string,
) *CopyFileTool {
	return &CopyFileTool{sysFs: buildFsWithMemoryRedirect(workspace, restrict, patterns, memoryRoot)}
}

func (t *CopyFileTool) Name() string {
	return "files_copy"
}

func (t *CopyFileTool) Description() string {
	return "Copy a file from source_path to destination_path. Preserves source file mode. Refuses directories."
}

func (t *CopyFileTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"source_path": map[string]any{
				"type":        "string",
				"description": "Workspace-relative path of the file to copy.",
			},
			"destination_path": map[string]any{
				"type":        "string",
				"description": "Workspace-relative path to copy the file to.",
			},
			"overwrite": map[string]any{
				"type":        "boolean",
				"description": "If true, replace destination when it exists. If false (default), error when destination exists.",
				"default":     false,
			},
			"display": map[string]any{
				"type":        "boolean",
				"description": "If true, after the operation, send the copied content to the user as a fenced block separated by `---` markers.",
				"default":     false,
			},
		},
		"required": []string{"source_path", "destination_path"},
	}
}

func (t *CopyFileTool) Execute(ctx context.Context, args map[string]any) *tools.ToolResult {
	src, ok := args["source_path"].(string)
	if !ok || src == "" {
		return tools.ErrorResult("source_path is required")
	}
	dst, ok := args["destination_path"].(string)
	if !ok || dst == "" {
		return tools.ErrorResult("destination_path is required")
	}
	overwrite := getBoolArg(args, "overwrite", false)

	data, err := copyFileViaFs(t.sysFs, src, dst, overwrite)
	if err != nil {
		return tools.ErrorResult(err.Error())
	}

	forLLM := fmt.Sprintf("File copied: %s -> %s", src, dst)
	if getBoolArg(args, "display", false) {
		return &tools.ToolResult{
			ForLLM:  forLLM,
			ForUser: displayBody("", string(data)),
		}
	}
	return tools.SilentResult(forLLM)
}

// copyFileViaFs copies src to dst through fsys, preserving source mode bits.
func copyFileViaFs(fsys fileSystem, src, dst string, overwrite bool) ([]byte, error) {
	if filepath.Clean(src) == filepath.Clean(dst) {
		return nil, fmt.Errorf("source and destination resolve to the same file: %s", filepath.Clean(src))
	}

	srcInfo, err := fsys.Stat(src)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || os.IsNotExist(err) {
			return nil, fmt.Errorf("source file not found: %s", src)
		}
		return nil, fmt.Errorf("failed to stat source: %w", err)
	}
	if srcInfo.IsDir() {
		return nil, fmt.Errorf("source is a directory; files_copy copies files only: %s", src)
	}

	dstInfo, dstErr := fsys.Stat(dst)
	if dstErr == nil {
		if dstInfo.IsDir() {
			return nil, fmt.Errorf("destination is a directory; refusing to overwrite: %s", dst)
		}
		if !overwrite {
			return nil, fmt.Errorf("destination already exists; pass overwrite=true to replace: %s", dst)
		}
	} else if !errors.Is(dstErr, fs.ErrNotExist) && !os.IsNotExist(dstErr) {
		return nil, fmt.Errorf("failed to stat destination: %w", dstErr)
	}

	data, err := fsys.ReadFile(src)
	if err != nil {
		return nil, err
	}
	if err := fsys.WriteFileMode(dst, data, srcInfo.Mode().Perm()); err != nil {
		return nil, err
	}
	return data, nil
}
