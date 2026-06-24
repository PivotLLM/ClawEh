package files

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"

	"github.com/PivotLLM/ClawEh/pkg/tools"
)

// backupFilePattern matches the numbered backup siblings written by the `backup`
// option (<name>.NNNN). file_delete refuses to remove these so the agent cannot
// wipe the safety net. (A legitimate file ending in .NNNN is conservatively
// protected too — rename it first if it genuinely must be deleted.)
var backupFilePattern = regexp.MustCompile(`\.\d{4}$`)

// DeleteFileTool deletes a whole file. A required sure=true guards against
// accidental deletion; it makes no backup and refuses to delete backup files.
type DeleteFileTool struct {
	sysFs fileSystem
}

func NewDeleteFileToolScoped(workspace string, restrict bool, writeSubdir string, allowPaths ...[]*regexp.Regexp) *DeleteFileTool {
	var patterns []*regexp.Regexp
	if len(allowPaths) > 0 {
		patterns = allowPaths[0]
	}
	return &DeleteFileTool{sysFs: buildWriteFs(workspace, restrict, writeSubdir, patterns)}
}

func (t *DeleteFileTool) Name() string { return "file_delete" }

func (t *DeleteFileTool) Description() string {
	return "Delete a file. Requires sure=true. Makes no backup, and refuses to delete backup files (<name>.NNNN)."
}

func (t *DeleteFileTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{"type": "string", "description": "Path of the file to delete."},
			"sure": map[string]any{"type": "boolean", "description": "Must be true to confirm deletion."},
		},
		"required": []string{"path", "sure"},
	}
}

func (t *DeleteFileTool) Execute(_ context.Context, args map[string]any) *tools.ToolResult {
	path, ok := args["path"].(string)
	if !ok || path == "" {
		return tools.ErrorResult("path is required")
	}
	if !getBoolArg(args, "sure", false) {
		return tools.ErrorResult("refusing to delete: pass sure=true to confirm deleting " + path)
	}
	if backupFilePattern.MatchString(filepath.Base(path)) {
		return tools.ErrorResult("refusing to delete a backup file (<name>.NNNN): " + path)
	}
	info, err := t.sysFs.Stat(path)
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	if info.IsDir() {
		return tools.ErrorResult("path is a directory; file_delete removes files only: " + path)
	}
	if err := t.sysFs.Remove(path); err != nil {
		return tools.ErrorResult(err.Error())
	}
	return tools.SilentResult(fmt.Sprintf("File deleted: %s", path))
}

// MoveFileTool relocates a file so the agent can organize files without reading
// them into context.
//
// DESIGN DECISION — move is implemented as copy-then-delete, NOT rename. A
// rename(2) fails with EXDEV across filesystems, and external mounts (e.g. notes/)
// commonly live on a different device than the workspace, so a rename-based move
// would silently break cross-mount moves. The files handled here are small, so the
// extra copy is not a concern. Do not "optimize" this into a rename.
type MoveFileTool struct {
	sysFs fileSystem
}

func NewMoveFileToolScoped(workspace string, restrict bool, writeSubdir string, allowPaths ...[]*regexp.Regexp) *MoveFileTool {
	var patterns []*regexp.Regexp
	if len(allowPaths) > 0 {
		patterns = allowPaths[0]
	}
	return &MoveFileTool{sysFs: buildWriteFs(workspace, restrict, writeSubdir, patterns)}
}

func (t *MoveFileTool) Name() string { return "file_move" }

func (t *MoveFileTool) Description() string {
	return "Move a file from source_path to destination_path to organize files (parent directories created as needed; works across mount boundaries; no content enters context)."
}

func (t *MoveFileTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"source_path":      map[string]any{"type": "string", "description": "Path of the file to move."},
			"destination_path": map[string]any{"type": "string", "description": "New path for the file."},
			"overwrite":        map[string]any{"type": "boolean", "description": "Replace destination if it exists (default false).", "default": false},
		},
		"required": []string{"source_path", "destination_path"},
	}
}

func (t *MoveFileTool) Execute(_ context.Context, args map[string]any) *tools.ToolResult {
	src, ok := args["source_path"].(string)
	if !ok || src == "" {
		return tools.ErrorResult("source_path is required")
	}
	dst, ok := args["destination_path"].(string)
	if !ok || dst == "" {
		return tools.ErrorResult("destination_path is required")
	}
	overwrite := getBoolArg(args, "overwrite", false)

	// Copy first; only remove the source once the copy has succeeded. See the
	// type comment for why this is copy-then-delete rather than rename.
	if _, err := copyFileViaFs(t.sysFs, src, dst, overwrite); err != nil {
		return tools.ErrorResult(err.Error())
	}
	if err := t.sysFs.Remove(src); err != nil {
		return tools.ErrorResult(fmt.Sprintf("copied to %s but failed to remove source %s: %v", dst, src, err))
	}
	return tools.SilentResult(fmt.Sprintf("File moved: %s -> %s", src, dst))
}
