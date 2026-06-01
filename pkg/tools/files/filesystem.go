package files

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/fileutil"
	"github.com/PivotLLM/ClawEh/pkg/logger"
	"github.com/PivotLLM/ClawEh/pkg/tools"
)

const MaxReadFileSize = 64 * 1024 // 64KB limit to avoid context overflow

// validatePath ensures the given path is within the workspace if restrict is true.
func validatePath(path, workspace string, restrict bool) (string, error) {
	if workspace == "" {
		return path, fmt.Errorf("workspace is not defined")
	}

	absWorkspace, err := filepath.Abs(workspace)
	if err != nil {
		return "", fmt.Errorf("failed to resolve workspace path: %w", err)
	}

	var absPath string
	if filepath.IsAbs(path) {
		absPath = filepath.Clean(path)
	} else {
		absPath, err = filepath.Abs(filepath.Join(absWorkspace, path))
		if err != nil {
			return "", fmt.Errorf("failed to resolve file path: %w", err)
		}
	}

	if restrict {
		if !isWithinWorkspace(absPath, absWorkspace) {
			return "", fmt.Errorf("access denied: path is outside the workspace")
		}

		var resolved string
		workspaceReal := absWorkspace
		if resolved, err = filepath.EvalSymlinks(absWorkspace); err == nil {
			workspaceReal = resolved
		}

		if resolved, err = filepath.EvalSymlinks(absPath); err == nil {
			if !isWithinWorkspace(resolved, workspaceReal) {
				return "", fmt.Errorf("access denied: symlink resolves outside workspace")
			}
		} else if os.IsNotExist(err) {
			var parentResolved string
			if parentResolved, err = resolveExistingAncestor(filepath.Dir(absPath)); err == nil {
				if !isWithinWorkspace(parentResolved, workspaceReal) {
					return "", fmt.Errorf("access denied: symlink resolves outside workspace")
				}
			} else if !os.IsNotExist(err) {
				return "", fmt.Errorf("failed to resolve path: %w", err)
			}
		} else {
			return "", fmt.Errorf("failed to resolve path: %w", err)
		}
	}

	return absPath, nil
}

func resolveExistingAncestor(path string) (string, error) {
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		if resolved, err := filepath.EvalSymlinks(current); err == nil {
			return resolved, nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
		if filepath.Dir(current) == current {
			return "", os.ErrNotExist
		}
	}
}

func isWithinWorkspace(candidate, workspace string) bool {
	rel, err := filepath.Rel(filepath.Clean(workspace), filepath.Clean(candidate))
	return err == nil && filepath.IsLocal(rel)
}

type ReadFileTool struct {
	sysFs   fileSystem
	maxSize int64
}

func NewReadFileTool(
	workspace string,
	restrict bool,
	maxReadFileSize int,
	allowPaths ...[]*regexp.Regexp,
) *ReadFileTool {
	var patterns []*regexp.Regexp
	if len(allowPaths) > 0 {
		patterns = allowPaths[0]
	}

	maxSize := int64(maxReadFileSize)
	if maxSize <= 0 {
		maxSize = MaxReadFileSize
	}

	return &ReadFileTool{
		sysFs:   buildFs(workspace, restrict, patterns),
		maxSize: maxSize,
	}
}

// NewReadFileToolWithMemoryRedirect is like NewReadFileTool but also redirects
// any access under the workspace-relative "memory/" subtree to memoryRoot.
func NewReadFileToolWithMemoryRedirect(
	workspace string,
	restrict bool,
	maxReadFileSize int,
	patterns []*regexp.Regexp,
	memoryRoot string,
) *ReadFileTool {
	maxSize := int64(maxReadFileSize)
	if maxSize <= 0 {
		maxSize = MaxReadFileSize
	}
	return &ReadFileTool{
		sysFs:   buildFsWithMemoryRedirect(workspace, restrict, patterns, memoryRoot),
		maxSize: maxSize,
	}
}

func (t *ReadFileTool) Name() string {
	return "files_read"
}

func (t *ReadFileTool) Description() string {
	return "Read the contents of a file. Supports pagination via `offset` and `length`."
}

func (t *ReadFileTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Path to the file to read.",
			},
			"offset": map[string]any{
				"type":        "integer",
				"description": "Byte offset to start reading from.",
				"default":     0,
			},
			"length": map[string]any{
				"type":        "integer",
				"description": "Maximum number of bytes to read.",
				"default":     t.maxSize,
			},
		},
		"required": []string{"path"},
	}
}

func (t *ReadFileTool) Execute(ctx context.Context, args map[string]any) *tools.ToolResult {
	path, ok := args["path"].(string)
	if !ok {
		return tools.ErrorResult("path is required")
	}

	// offset (optional, default 0)
	offset, err := getInt64Arg(args, "offset", 0)
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	if offset < 0 {
		return tools.ErrorResult("offset must be >= 0")
	}

	// length (optional, capped at MaxReadFileSize)
	length, err := getInt64Arg(args, "length", t.maxSize)
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	if length <= 0 {
		return tools.ErrorResult("length must be > 0")
	}
	if length > t.maxSize {
		length = t.maxSize
	}

	file, err := t.sysFs.Open(path)
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	defer file.Close()

	// measure total size
	totalSize := int64(-1) // -1 means unknown
	if info, statErr := file.Stat(); statErr == nil {
		totalSize = info.Size()
	}

	// sniff the first 512 bytes to detect binary content before loading
	// it into the LLM context. Seeking back to 0 afterwards restores state.
	sniff := make([]byte, 512)
	sniffN, _ := file.Read(sniff)

	// Reset read position to beginning before applying the caller's offset.
	if seeker, ok := file.(io.Seeker); ok {
		_, err = seeker.Seek(0, io.SeekStart)
		if err != nil {
			return tools.ErrorResult(fmt.Sprintf("failed to reset file position after sniff: %v", err))
		}
	} else {
		if offset < int64(sniffN) && offset > 0 {
			return tools.ErrorResult(
				"non-seekable file: cannot seek to an offset within the first 512 bytes after binary detection",
			)
		}
	}

	// Seek to the requested offset.
	if seeker, ok := file.(io.Seeker); ok {
		_, err = seeker.Seek(offset, io.SeekStart)
		if err != nil {
			return tools.ErrorResult(fmt.Sprintf("failed to seek to offset %d: %v", offset, err))
		}
	} else if offset > 0 {
		remaining := offset - int64(sniffN)
		if remaining > 0 {
			_, err = io.CopyN(io.Discard, file, remaining)
			if err != nil {
				return tools.ErrorResult(fmt.Sprintf("failed to advance to offset %d: %v", offset, err))
			}
		}
	}

	probe := make([]byte, length+1)
	n, err := io.ReadFull(file, probe)
	if err != nil && err != io.EOF && !errors.Is(err, io.ErrUnexpectedEOF) {
		return tools.ErrorResult(fmt.Sprintf("failed to read file content: %v", err))
	}

	hasMore := int64(n) > length
	data := probe[:min(int64(n), length)]

	if len(data) == 0 {
		return tools.NewToolResult("[END OF FILE - no content at this offset]")
	}

	readEnd := offset + int64(len(data))
	readRange := fmt.Sprintf("bytes %d-%d", offset, readEnd-1)

	displayPath := filepath.Base(path)
	var header string
	if totalSize >= 0 {
		header = fmt.Sprintf(
			"[file: %s | total: %d bytes | read: %s]",
			displayPath, totalSize, readRange,
		)
	} else {
		header = fmt.Sprintf(
			"[file: %s | read: %s | total size unknown]",
			displayPath, readRange,
		)
	}

	if hasMore {
		header += fmt.Sprintf(
			"\n[TRUNCATED - file has more content. Call files_read again with offset=%d to continue.]",
			readEnd,
		)
	} else {
		header += "\n[END OF FILE - no further content.]"
	}

	logger.DebugCF("tool", "ReadFileTool execution completed successfully",
		map[string]any{
			"path":       path,
			"bytes_read": len(data),
			"has_more":   hasMore,
		})

	return tools.NewToolResult(header + "\n\n" + string(data))
}

// getInt64Arg extracts an integer argument from the args map, returning the
// provided default if the key is absent.
func getInt64Arg(args map[string]any, key string, defaultVal int64) (int64, error) {
	raw, exists := args[key]
	if !exists {
		return defaultVal, nil
	}

	switch v := raw.(type) {
	case float64:
		if v != math.Trunc(v) {
			return 0, fmt.Errorf("%s must be an integer, got float %v", key, v)
		}
		if v > math.MaxInt64 || v < math.MinInt64 {
			return 0, fmt.Errorf("%s value %v overflows int64", key, v)
		}
		return int64(v), nil
	case int:
		return int64(v), nil
	case int64:
		return v, nil
	case string:
		parsed, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid integer format for %s parameter: %w", key, err)
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("unsupported type %T for %s parameter", raw, key)
	}
}

type WriteFileTool struct {
	sysFs fileSystem
}

func NewWriteFileTool(workspace string, restrict bool, allowPaths ...[]*regexp.Regexp) *WriteFileTool {
	var patterns []*regexp.Regexp
	if len(allowPaths) > 0 {
		patterns = allowPaths[0]
	}
	return &WriteFileTool{sysFs: buildFs(workspace, restrict, patterns)}
}

func NewWriteFileToolWithMemoryRedirect(
	workspace string,
	restrict bool,
	patterns []*regexp.Regexp,
	memoryRoot string,
) *WriteFileTool {
	return &WriteFileTool{sysFs: buildFsWithMemoryRedirect(workspace, restrict, patterns, memoryRoot)}
}

func (t *WriteFileTool) Name() string {
	return "files_write"
}

func (t *WriteFileTool) Description() string {
	return "Write content to a file. Refuses to replace an existing file unless overwrite is true."
}

func (t *WriteFileTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Path to the file to write",
			},
			"content": map[string]any{
				"type":        "string",
				"description": "Content to write to the file",
			},
			"overwrite": map[string]any{
				"type":        "boolean",
				"description": "If the target file already exists and overwrite is not true, the call fails. Set overwrite: true to replace an existing file.",
				"default":     false,
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

func (t *WriteFileTool) Execute(ctx context.Context, args map[string]any) *tools.ToolResult {
	path, ok := args["path"].(string)
	if !ok {
		return tools.ErrorResult("path is required")
	}

	content, ok := args["content"].(string)
	if !ok {
		return tools.ErrorResult("content is required")
	}

	if !getBoolArg(args, "overwrite", false) {
		if info, err := t.sysFs.Stat(path); err == nil && !info.IsDir() {
			return tools.ErrorResult(fmt.Sprintf("file already exists: %s. Set overwrite: true to replace it.", path))
		}
	}

	if getBoolArg(args, "backup", false) {
		if _, err := backupExistingFile(t.sysFs, path); err != nil {
			return tools.ErrorResult(err.Error())
		}
	}

	if err := t.sysFs.WriteFile(path, []byte(content)); err != nil {
		return tools.ErrorResult(err.Error())
	}

	forLLM := fmt.Sprintf("File written: %s", path)
	if getBoolArg(args, "display", false) {
		return &tools.ToolResult{
			ForLLM:  forLLM,
			ForUser: displayBody(displayHeader("Wrote", path), content),
		}
	}
	return tools.SilentResult(forLLM)
}

type ListDirTool struct {
	sysFs fileSystem
}

func NewListDirTool(workspace string, restrict bool, allowPaths ...[]*regexp.Regexp) *ListDirTool {
	var patterns []*regexp.Regexp
	if len(allowPaths) > 0 {
		patterns = allowPaths[0]
	}
	return &ListDirTool{sysFs: buildFs(workspace, restrict, patterns)}
}

func NewListDirToolWithMemoryRedirect(
	workspace string,
	restrict bool,
	patterns []*regexp.Regexp,
	memoryRoot string,
) *ListDirTool {
	return &ListDirTool{sysFs: buildFsWithMemoryRedirect(workspace, restrict, patterns, memoryRoot)}
}

func (t *ListDirTool) Name() string {
	return "files_list"
}

func (t *ListDirTool) Description() string {
	return "List files and directories in a path"
}

func (t *ListDirTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Path to list",
			},
		},
		"required": []string{"path"},
	}
}

func (t *ListDirTool) Execute(ctx context.Context, args map[string]any) *tools.ToolResult {
	path, ok := args["path"].(string)
	if !ok {
		path = "."
	}

	entries, err := t.sysFs.ReadDir(path)
	if err != nil {
		return tools.ErrorResult(fmt.Sprintf("failed to read directory: %v", err))
	}
	return formatDirEntries(entries)
}

func formatDirEntries(entries []os.DirEntry) *tools.ToolResult {
	var result strings.Builder
	for _, entry := range entries {
		if entry.IsDir() {
			result.WriteString("DIR:  " + entry.Name() + "\n")
		} else {
			result.WriteString("FILE: " + entry.Name() + "\n")
		}
	}
	return tools.NewToolResult(result.String())
}

// fileSystem abstracts reading, writing, and listing files.
type fileSystem interface {
	ReadFile(path string) ([]byte, error)
	WriteFile(path string, data []byte) error
	WriteFileMode(path string, data []byte, mode os.FileMode) error
	WriteFileExclMode(path string, data []byte, mode os.FileMode) error
	ReadDir(path string) ([]os.DirEntry, error)
	Open(path string) (fs.File, error)
	Stat(path string) (os.FileInfo, error)
}

// hostFs is an unrestricted fileReadWriter that operates directly on the host filesystem.
type hostFs struct{}

func (h *hostFs) ReadFile(path string) ([]byte, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("failed to read file: file not found: %w", err)
		}
		if os.IsPermission(err) {
			return nil, fmt.Errorf("failed to read file: access denied: %w", err)
		}
		return nil, fmt.Errorf("failed to read file: %w", err)
	}
	return content, nil
}

func (h *hostFs) ReadDir(path string) ([]os.DirEntry, error) {
	return os.ReadDir(path)
}

func (h *hostFs) WriteFile(path string, data []byte) error {
	return fileutil.WriteFileAtomic(path, data, 0o600)
}

func (h *hostFs) WriteFileMode(path string, data []byte, mode os.FileMode) error {
	return fileutil.WriteFileAtomic(path, data, mode)
}

func (h *hostFs) WriteFileExclMode(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return err
	}
	if err := os.Chmod(path, mode); err != nil {
		os.Remove(path)
		return err
	}
	return nil
}

func (h *hostFs) Stat(path string) (os.FileInfo, error) {
	return os.Stat(path)
}

func (h *hostFs) Open(path string) (fs.File, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("failed to open file: file not found: %w", err)
		}
		if os.IsPermission(err) {
			return nil, fmt.Errorf("failed to open file: access denied: %w", err)
		}
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	return f, nil
}

// sandboxFs is a sandboxed fileSystem that operates within a strictly defined workspace using os.Root.
type sandboxFs struct {
	workspace string
}

func (r *sandboxFs) execute(path string, fn func(root *os.Root, relPath string) error) error {
	if r.workspace == "" {
		return fmt.Errorf("workspace is not defined")
	}

	root, err := os.OpenRoot(r.workspace)
	if err != nil {
		return fmt.Errorf("failed to open workspace: %w", err)
	}
	defer root.Close()

	relPath, err := getSafeRelPath(r.workspace, path)
	if err != nil {
		return err
	}

	return fn(root, relPath)
}

func (r *sandboxFs) ReadFile(path string) ([]byte, error) {
	var content []byte
	err := r.execute(path, func(root *os.Root, relPath string) error {
		fileContent, err := root.ReadFile(relPath)
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("failed to read file: file not found: %w", err)
			}
			if os.IsPermission(err) || strings.Contains(err.Error(), "escapes from parent") ||
				strings.Contains(err.Error(), "permission denied") {
				return fmt.Errorf("failed to read file: access denied: %w", err)
			}
			return fmt.Errorf("failed to read file: %w", err)
		}
		content = fileContent
		return nil
	})
	return content, err
}

func (r *sandboxFs) WriteFile(path string, data []byte) error {
	return r.WriteFileMode(path, data, 0o600)
}

func (r *sandboxFs) WriteFileMode(path string, data []byte, mode os.FileMode) error {
	return r.execute(path, func(root *os.Root, relPath string) error {
		dir := filepath.Dir(relPath)
		if dir != "." && dir != "/" {
			if err := root.MkdirAll(dir, 0o755); err != nil {
				return fmt.Errorf("failed to create parent directories: %w", err)
			}
		}

		tmpRelPath := fmt.Sprintf(".tmp-%d-%d", os.Getpid(), time.Now().UnixNano())

		tmpFile, err := root.OpenFile(tmpRelPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err != nil {
			root.Remove(tmpRelPath)
			return fmt.Errorf("failed to open temp file: %w", err)
		}

		if _, err := tmpFile.Write(data); err != nil {
			tmpFile.Close()
			root.Remove(tmpRelPath)
			return fmt.Errorf("failed to write temp file: %w", err)
		}

		if err := tmpFile.Sync(); err != nil {
			tmpFile.Close()
			root.Remove(tmpRelPath)
			return fmt.Errorf("failed to sync temp file: %w", err)
		}

		if err := tmpFile.Close(); err != nil {
			root.Remove(tmpRelPath)
			return fmt.Errorf("failed to close temp file: %w", err)
		}

		if err := root.Chmod(tmpRelPath, mode); err != nil {
			root.Remove(tmpRelPath)
			return fmt.Errorf("failed to set file mode: %w", err)
		}

		if err := root.Rename(tmpRelPath, relPath); err != nil {
			root.Remove(tmpRelPath)
			return fmt.Errorf("failed to rename temp file over target: %w", err)
		}

		if dirFile, err := root.Open("."); err == nil {
			_ = dirFile.Sync()
			dirFile.Close()
		}

		return nil
	})
}

func (r *sandboxFs) WriteFileExclMode(path string, data []byte, mode os.FileMode) error {
	return r.execute(path, func(root *os.Root, relPath string) error {
		dir := filepath.Dir(relPath)
		if dir != "." && dir != "/" {
			if err := root.MkdirAll(dir, 0o755); err != nil {
				return fmt.Errorf("failed to create parent directories: %w", err)
			}
		}
		f, err := root.OpenFile(relPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err != nil {
			return err
		}
		if _, err := f.Write(data); err != nil {
			f.Close()
			root.Remove(relPath)
			return err
		}
		if err := f.Sync(); err != nil {
			f.Close()
			root.Remove(relPath)
			return err
		}
		if err := f.Close(); err != nil {
			root.Remove(relPath)
			return err
		}
		if err := root.Chmod(relPath, mode); err != nil {
			root.Remove(relPath)
			return err
		}
		return nil
	})
}

func (r *sandboxFs) Stat(path string) (os.FileInfo, error) {
	var info os.FileInfo
	err := r.execute(path, func(root *os.Root, relPath string) error {
		fi, err := root.Stat(relPath)
		if err != nil {
			return err
		}
		info = fi
		return nil
	})
	return info, err
}

func (r *sandboxFs) ReadDir(path string) ([]os.DirEntry, error) {
	var entries []os.DirEntry
	err := r.execute(path, func(root *os.Root, relPath string) error {
		dirEntries, err := fs.ReadDir(root.FS(), relPath)
		if err != nil {
			return err
		}
		entries = dirEntries
		return nil
	})
	return entries, err
}

func (r *sandboxFs) Open(path string) (fs.File, error) {
	var f fs.File
	err := r.execute(path, func(root *os.Root, relPath string) error {
		file, err := root.Open(relPath)
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("failed to open file: file not found: %w", err)
			}
			if os.IsPermission(err) || strings.Contains(err.Error(), "escapes from parent") ||
				strings.Contains(err.Error(), "permission denied") {
				return fmt.Errorf("failed to open file: access denied: %w", err)
			}
			return fmt.Errorf("failed to open file: %w", err)
		}
		f = file
		return nil
	})
	return f, err
}

// whitelistFs wraps a sandboxFs and allows access to specific paths outside
// the workspace when they match any of the provided patterns.
type whitelistFs struct {
	sandbox  *sandboxFs
	host     hostFs
	patterns []*regexp.Regexp
}

func (w *whitelistFs) matches(path string) bool {
	for _, p := range w.patterns {
		if p.MatchString(path) {
			return true
		}
	}
	return false
}

func (w *whitelistFs) ReadFile(path string) ([]byte, error) {
	if w.matches(path) {
		return w.host.ReadFile(path)
	}
	return w.sandbox.ReadFile(path)
}

func (w *whitelistFs) WriteFile(path string, data []byte) error {
	if w.matches(path) {
		return w.host.WriteFile(path, data)
	}
	return w.sandbox.WriteFile(path, data)
}

func (w *whitelistFs) WriteFileMode(path string, data []byte, mode os.FileMode) error {
	if w.matches(path) {
		return w.host.WriteFileMode(path, data, mode)
	}
	return w.sandbox.WriteFileMode(path, data, mode)
}

func (w *whitelistFs) WriteFileExclMode(path string, data []byte, mode os.FileMode) error {
	if w.matches(path) {
		return w.host.WriteFileExclMode(path, data, mode)
	}
	return w.sandbox.WriteFileExclMode(path, data, mode)
}

func (w *whitelistFs) Stat(path string) (os.FileInfo, error) {
	if w.matches(path) {
		return w.host.Stat(path)
	}
	return w.sandbox.Stat(path)
}

func (w *whitelistFs) ReadDir(path string) ([]os.DirEntry, error) {
	if w.matches(path) {
		return w.host.ReadDir(path)
	}
	return w.sandbox.ReadDir(path)
}

func (w *whitelistFs) Open(path string) (fs.File, error) {
	if w.matches(path) {
		return w.host.Open(path)
	}
	return w.sandbox.Open(path)
}

// buildFs returns the appropriate fileSystem implementation based on restriction
// settings and optional path whitelist patterns.
func buildFs(workspace string, restrict bool, patterns []*regexp.Regexp) fileSystem {
	if !restrict {
		return &hostFs{}
	}
	sandbox := &sandboxFs{workspace: workspace}
	if len(patterns) > 0 {
		return &whitelistFs{sandbox: sandbox, patterns: patterns}
	}
	return sandbox
}

// redirectFs transparently rewrites any access whose path is workspace-relative
// "memory" (or starts with "memory/") so it lands under memoryRoot instead of
// <workspace>/memory.
type redirectFs struct {
	base       fileSystem
	mem        fileSystem
	workspace  string
	memoryRoot string
}

func (r *redirectFs) rewrite(path string) (fileSystem, string) {
	if r.memoryRoot == "" {
		return r.base, path
	}

	var rel string
	if filepath.IsAbs(path) {
		cleaned := filepath.Clean(path)
		rr, err := filepath.Rel(r.workspace, cleaned)
		if err != nil || !filepath.IsLocal(rr) {
			return r.base, path
		}
		rel = rr
	} else {
		rel = filepath.Clean(path)
		if !filepath.IsLocal(rel) {
			return r.base, path
		}
	}

	slashed := filepath.ToSlash(rel)
	if slashed == "memory" {
		return r.mem, "."
	}
	if strings.HasPrefix(slashed, "memory/") {
		sub := strings.TrimPrefix(slashed, "memory/")
		if sub == "" {
			return r.mem, "."
		}
		return r.mem, filepath.FromSlash(sub)
	}
	return r.base, path
}

func (r *redirectFs) ReadFile(path string) ([]byte, error) {
	fsys, p := r.rewrite(path)
	return fsys.ReadFile(p)
}

func (r *redirectFs) WriteFile(path string, data []byte) error {
	fsys, p := r.rewrite(path)
	return fsys.WriteFile(p, data)
}

func (r *redirectFs) WriteFileMode(path string, data []byte, mode os.FileMode) error {
	fsys, p := r.rewrite(path)
	return fsys.WriteFileMode(p, data, mode)
}

func (r *redirectFs) WriteFileExclMode(path string, data []byte, mode os.FileMode) error {
	fsys, p := r.rewrite(path)
	return fsys.WriteFileExclMode(p, data, mode)
}

func (r *redirectFs) Stat(path string) (os.FileInfo, error) {
	fsys, p := r.rewrite(path)
	return fsys.Stat(p)
}

func (r *redirectFs) ReadDir(path string) ([]os.DirEntry, error) {
	fsys, p := r.rewrite(path)
	return fsys.ReadDir(p)
}

func (r *redirectFs) Open(path string) (fs.File, error) {
	fsys, p := r.rewrite(path)
	return fsys.Open(p)
}

func buildFsWithMemoryRedirect(workspace string, restrict bool, patterns []*regexp.Regexp, memoryRoot string) fileSystem {
	base := buildFs(workspace, restrict, patterns)
	if !restrict || strings.TrimSpace(memoryRoot) == "" {
		return base
	}

	absWS, err := filepath.Abs(workspace)
	if err != nil {
		return base
	}
	absMem, err := filepath.Abs(memoryRoot)
	if err != nil {
		return base
	}
	if absMem == filepath.Join(absWS, "memory") {
		return base
	}

	return &redirectFs{
		base:       base,
		mem:        &sandboxFs{workspace: absMem},
		workspace:  absWS,
		memoryRoot: absMem,
	}
}

// Helper to get a safe relative path for os.Root usage
func getSafeRelPath(workspace, path string) (string, error) {
	if workspace == "" {
		return "", fmt.Errorf("workspace is not defined")
	}

	rel := filepath.Clean(path)
	if filepath.IsAbs(rel) {
		var err error
		rel, err = filepath.Rel(workspace, rel)
		if err != nil {
			return "", fmt.Errorf("failed to calculate relative path: %w", err)
		}
	}

	if !filepath.IsLocal(rel) {
		return "", fmt.Errorf("path escapes workspace: %s", path)
	}

	return rel, nil
}
