package files

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/PivotLLM/ClawEh/tools"
)

// MkdirTool creates a folder, and any missing parents, inside the write scope.
type MkdirTool struct {
	sysFs fileSystem
}

func NewMkdirToolScoped(workspace string, restrict bool, writeSubdir string, allowPaths ...[]*regexp.Regexp) *MkdirTool {
	var patterns []*regexp.Regexp
	if len(allowPaths) > 0 {
		patterns = allowPaths[0]
	}
	return &MkdirTool{sysFs: buildWriteFs(workspace, restrict, writeSubdir, patterns)}
}

func (t *MkdirTool) Name() string { return "file_mkdir" }

func (t *MkdirTool) Description() string {
	return "Create a folder, and any missing parent folders. Succeeds if the folder already exists."
}

func (t *MkdirTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{"type": "string", "description": "Path of the folder to create."},
		},
		"required": []string{"path"},
	}
}

func (t *MkdirTool) Execute(_ context.Context, args map[string]any) *tools.ToolResult {
	path, ok := args["path"].(string)
	if !ok || path == "" {
		return tools.ErrorResult("path is required")
	}
	if err := checkWrite(t.sysFs, path); err != nil {
		return errResult(err)
	}
	info, err := t.sysFs.Stat(path)
	switch {
	case err == nil && info.IsDir():
		return tools.SilentResult("Folder already exists: " + path)
	case err == nil:
		return errResult(tools.Refusal(errors.New("path exists and is a file: " + path)))
	case !errors.Is(err, fs.ErrNotExist):
		return errResult(err)
	}
	if err := t.sysFs.MkdirAll(path); err != nil {
		return errResult(err)
	}
	return tools.SilentResult("Folder created: " + path)
}

// RmdirTool removes a folder: an empty one, or with recursive=true the whole
// tree. A required sure=true guards against accidental removal; it refuses to
// remove backup files (<name>.NNNN), the write folder itself and mount roots.
type RmdirTool struct {
	sysFs fileSystem
}

func NewRmdirToolScoped(workspace string, restrict bool, writeSubdir string, allowPaths ...[]*regexp.Regexp) *RmdirTool {
	var patterns []*regexp.Regexp
	if len(allowPaths) > 0 {
		patterns = allowPaths[0]
	}
	return &RmdirTool{sysFs: buildWriteFs(workspace, restrict, writeSubdir, patterns)}
}

func (t *RmdirTool) Name() string { return "file_rmdir" }

func (t *RmdirTool) Description() string {
	return "Remove a folder (folders only; delete files with file_delete). Requires sure=true. Removes only an empty folder unless recursive=true. Makes no backup, and refuses to remove backup files (<name>.NNNN)."
}

func (t *RmdirTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":      map[string]any{"type": "string", "description": "Path of the folder to remove."},
			"sure":      map[string]any{"type": "boolean", "description": "Must be true to confirm removal."},
			"recursive": map[string]any{"type": "boolean", "description": "Also remove everything inside the folder (default false).", "default": false},
		},
		"required": []string{"path", "sure"},
	}
}

func (t *RmdirTool) Execute(_ context.Context, args map[string]any) *tools.ToolResult {
	path, ok := args["path"].(string)
	if !ok || path == "" {
		return tools.ErrorResult("path is required")
	}
	if !getBoolArg(args, "sure", false) {
		return errResult(tools.Refusal(errors.New("refusing to remove: pass sure=true to confirm removing " + path)))
	}
	if err := checkWrite(t.sysFs, path); err != nil {
		return errResult(err)
	}
	if isRoot(t.sysFs, path) {
		return errResult(tools.Refusal(errors.New("refusing to remove a top-level folder (the write folder or a mount): " + path)))
	}
	info, statErr := t.sysFs.Stat(path)
	if statErr != nil {
		return errResult(statErr)
	}
	if !info.IsDir() {
		return errResult(tools.Refusal(errors.New("path is a file; file_rmdir removes folders only, use file_delete for files: " + path)))
	}

	if !getBoolArg(args, "recursive", false) {
		entries, err := t.sysFs.ReadDir(path)
		if err != nil {
			return errResult(err)
		}
		if len(entries) > 0 {
			return errResult(tools.Refusal(errors.New("folder is not empty; pass recursive=true to remove it and everything in it: " + path)))
		}
		if err := t.sysFs.Remove(path); err != nil {
			return errResult(err)
		}
		return tools.SilentResult("Folder removed: " + path)
	}

	tree, err := walkTree(t.sysFs, path)
	if err != nil {
		return errResult(err)
	}
	// Refuse before removing anything, so a refusal leaves the tree intact.
	for _, f := range tree.files {
		full := filepath.Join(path, f)
		if backupFilePattern.MatchString(filepath.Base(f)) {
			return errResult(tools.Refusal(errors.New("refusing to remove: the folder contains a backup file (<name>.NNNN): " + full)))
		}
		if err := checkWrite(t.sysFs, full); err != nil {
			return errResult(err)
		}
	}
	for _, d := range tree.dirs {
		if err := checkWrite(t.sysFs, filepath.Join(path, d)); err != nil {
			return errResult(err)
		}
	}

	// Files first, then folders deepest first, then the folder itself.
	victims := make([]string, 0, len(tree.files)+len(tree.dirs)+1)
	victims = append(victims, tree.files...)
	for _, d := range slices.Backward(tree.dirs) {
		victims = append(victims, d)
	}
	victims = append(victims, ".")
	for i, v := range victims {
		if err := t.sysFs.Remove(filepath.Join(path, v)); err != nil {
			return tools.ErrorResult(fmt.Sprintf("removed %d of %d entries in %s, then failed on %s: %v; the rest is still there",
				i, len(victims), path, filepath.Join(path, v), err)).WithError(err)
		}
	}
	return tools.SilentResult(fmt.Sprintf("Folder removed: %s (%d files)", path, len(tree.files)))
}

// dirTree lists what is under a folder, by path relative to that folder.
type dirTree struct {
	dirs  []string // parents before their children
	files []string // everything that is not a folder, links included
}

// walkTree lists the tree under root through fsys, so every layer's path
// policy applies to every entry. Links are listed as files, not followed.
func walkTree(fsys fileSystem, root string) (dirTree, error) {
	var tree dirTree
	var walk func(rel string) error
	walk = func(rel string) error {
		entries, err := fsys.ReadDir(filepath.Join(root, rel))
		if err != nil {
			return err
		}
		for _, e := range entries {
			child := filepath.Join(rel, e.Name())
			if !e.IsDir() {
				tree.files = append(tree.files, child)
				continue
			}
			tree.dirs = append(tree.dirs, child)
			if err := walk(child); err != nil {
				return err
			}
		}
		return nil
	}
	return tree, walk(".")
}

// isInside reports whether dst is the folder src or lies inside it. It compares
// the folders themselves (dst and each existing parent of it against src), not
// the path strings, so a relative src and an absolute dst are caught too.
func isInside(fsys fileSystem, src, dst string) bool {
	srcInfo, err := fsys.Stat(src)
	if err != nil {
		return false
	}
	for p := filepath.Clean(dst); ; p = filepath.Dir(p) {
		if info, err := fsys.Stat(p); err == nil && os.SameFile(srcInfo, info) {
			return true
		}
		if filepath.Dir(p) == p {
			return false
		}
	}
}

// moveDir moves the folder src to dst one file at a time with copy-then-delete
// (see MoveFileTool for why it never renames), then removes the emptied source
// folders. Everything that could refuse is checked before the first copy; a
// later failure stops the move and reports what was moved and what was not.
func (t *MoveFileTool) moveDir(src, dst string, overwrite bool) *tools.ToolResult {
	if isInside(t.sysFs, src, dst) {
		return errResult(tools.Refusal(fmt.Errorf("cannot move a folder into itself: %s -> %s", src, dst)))
	}
	if isRoot(t.sysFs, src) {
		return errResult(tools.Refusal(errors.New("refusing to move a top-level folder (the write folder or a mount): " + src)))
	}

	for _, p := range []string{src, dst} {
		if err := checkWrite(t.sysFs, p); err != nil {
			return errResult(err)
		}
	}

	dstInfo, err := t.sysFs.Stat(dst)
	switch {
	case err == nil && !dstInfo.IsDir():
		return errResult(fmt.Errorf("destination is a file: %s", dst))
	case err == nil && !overwrite:
		return errResult(fmt.Errorf("destination already exists; pass overwrite=true to merge into it: %s", dst))
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return errResult(fmt.Errorf("failed to stat destination: %w", err))
	}
	dstExists := err == nil

	tree, err := walkTree(t.sysFs, src)
	if err != nil {
		return errResult(err)
	}

	// Pre-flight: the rest of the source must be removable and the rest of the
	// destination writable, and a merge must not land a file on a folder (or
	// the reverse) or overwrite a backup file.
	for _, d := range tree.dirs {
		target := filepath.Join(dst, d)
		for _, p := range []string{filepath.Join(src, d), target} {
			if err := checkWrite(t.sysFs, p); err != nil {
				return errResult(err)
			}
		}
		if dstExists {
			if info, err := t.sysFs.Stat(target); err == nil && !info.IsDir() {
				return errResult(fmt.Errorf("destination has a file where a folder goes: %s", target))
			}
		}
	}
	for _, f := range tree.files {
		target := filepath.Join(dst, f)
		for _, p := range []string{filepath.Join(src, f), target} {
			if err := checkWrite(t.sysFs, p); err != nil {
				return errResult(err)
			}
		}
		if !dstExists {
			continue
		}
		info, err := t.sysFs.Stat(target)
		if err != nil {
			continue
		}
		if info.IsDir() {
			return errResult(fmt.Errorf("destination has a folder where a file goes: %s", target))
		}
		if backupFilePattern.MatchString(filepath.Base(f)) {
			return errResult(tools.Refusal(errors.New("refusing to overwrite a backup file (<name>.NNNN): " + target)))
		}
	}

	// Create every destination folder (empty ones included), then move the
	// files, then remove the emptied source folders deepest first.
	moved := 0
	fail := func(what string, err error) *tools.ToolResult {
		movedList, notMovedList := "none", "none"
		if moved > 0 {
			movedList = strings.Join(tree.files[:moved], ", ")
		}
		if moved < len(tree.files) {
			notMovedList = strings.Join(tree.files[moved:], ", ")
		}
		return tools.ErrorResult(fmt.Sprintf(
			"folder move stopped at %s: %v. Moved %d of %d files to %s: %s. Not moved, still in %s: %s.",
			what, err, moved, len(tree.files), dst, movedList, src, notMovedList)).WithError(err)
	}
	if err := t.sysFs.MkdirAll(dst); err != nil {
		return fail(dst, err)
	}
	for _, d := range tree.dirs {
		if err := t.sysFs.MkdirAll(filepath.Join(dst, d)); err != nil {
			return fail(filepath.Join(dst, d), err)
		}
	}
	for _, f := range tree.files {
		from := filepath.Join(src, f)
		if _, err := copyFileViaFs(t.sysFs, from, filepath.Join(dst, f), overwrite); err != nil {
			return fail(from, err)
		}
		if err := t.sysFs.Remove(from); err != nil {
			// The copy exists but the source could not be removed: neither
			// moved nor untouched, so name it on its own.
			return fail(from, fmt.Errorf("copied but failed to remove the source: %w", err))
		}
		moved++
	}
	for _, d := range slices.Backward(tree.dirs) {
		if err := t.sysFs.Remove(filepath.Join(src, d)); err != nil {
			return fail(filepath.Join(src, d), err)
		}
	}
	if err := t.sysFs.Remove(src); err != nil {
		return fail(src, err)
	}
	return tools.SilentResult(fmt.Sprintf("Folder moved: %s -> %s (%d files)", src, dst, len(tree.files)))
}
