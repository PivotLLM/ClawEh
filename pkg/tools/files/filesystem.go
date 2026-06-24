package files

import (
	"bufio"
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
	"sync/atomic"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/fileutil"
	"github.com/PivotLLM/ClawEh/pkg/logger"
	"github.com/PivotLLM/ClawEh/pkg/tools"
)

const MaxReadFileSize = 32 * 1024 // 32KB (~8K tokens) per-read cap to avoid context overflow

// defaultReadLineCount is how many lines file_read_lines returns when
// line_count is unspecified. Output is still capped to the byte ceiling.
const defaultReadLineCount = 250

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
	sysFs    fileSystem
	maxSize  int64
	lineMode bool // true => file_read_lines (line addressing); false => file_read_bytes
}

// NewReadFileTool builds the byte-addressed read tool (file_read_bytes).
func NewReadFileTool(
	workspace string,
	restrict bool,
	maxReadFileSize int,
	allowPaths ...[]*regexp.Regexp,
) *ReadFileTool {
	return newReadTool(false, workspace, restrict, maxReadFileSize, allowPaths...)
}

// NewReadLinesTool builds the line-addressed read tool (file_read_lines).
func NewReadLinesTool(
	workspace string,
	restrict bool,
	maxReadFileSize int,
	allowPaths ...[]*regexp.Regexp,
) *ReadFileTool {
	return newReadTool(true, workspace, restrict, maxReadFileSize, allowPaths...)
}

func newReadTool(
	lineMode bool,
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
		sysFs:    buildFs(workspace, restrict, patterns),
		maxSize:  maxSize,
		lineMode: lineMode,
	}
}

func (t *ReadFileTool) Name() string {
	if t.lineMode {
		return "file_read_lines"
	}
	return "file_read_bytes"
}

func (t *ReadFileTool) Description() string {
	if t.lineMode {
		return "Read a file by LINE number. Pass `start_line` (1-based) and optional `line_count` to read a " +
			"numbered slice; lines are returned with their numbers, and the status block tells you the next " +
			"`start_line`. Best for text/code and for getting exact text to pass to file_edit. " +
			"Line numbers from file_search_lines feed directly into this tool. " +
			"(To page by raw bytes instead, use file_read_bytes.)"
	}
	return "Read a file by BYTE offset. Pass `offset` and optional `length` to read a byte range; the status " +
		"block tells you the next `offset`. Best for binary or very large files. " +
		"Byte offsets from file_search_bytes feed directly into this tool. " +
		"(For human-readable line addressing, use file_read_lines.)"
}

func (t *ReadFileTool) Parameters() map[string]any {
	if t.lineMode {
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path to the file to read.",
				},
				"start_line": map[string]any{
					"type":        "integer",
					"description": "1-based line to start from (default 1).",
					"default":     1,
				},
				"line_count": map[string]any{
					"type":        "integer",
					"description": "Number of lines to read from start_line (default 250). Still capped to the byte limit.",
					"default":     defaultReadLineCount,
				},
			},
			"required": []string{"path"},
		}
	}
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

	// Keep the two addressing modes pure: reject the other mode's parameters
	// rather than silently ignoring them (a stray start_line on file_read_bytes
	// would otherwise read from offset 0 — the exact front-chunk trap this split
	// exists to prevent). Point the caller at the right tool.
	if t.lineMode {
		if argPresent(args, "offset") || argPresent(args, "length") {
			return tools.ErrorResult("file_read_lines reads by line and does not accept offset/length. " +
				"Use file_read_lines(path, start_line[, line_count]); to read by byte use file_read_bytes(path, offset).")
		}
	} else if argPresent(args, "start_line") || argPresent(args, "line_count") {
		return tools.ErrorResult("file_read_bytes reads by byte offset and does not accept start_line/line_count. " +
			"Use file_read_bytes(path, offset[, length]); to read by line use file_read_lines(path, start_line).")
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

	// Line mode (file_read_lines): read a numbered slice of lines.
	startLine, err := getInt64Arg(args, "start_line", 1)
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	lineCount, err := getInt64Arg(args, "line_count", defaultReadLineCount)
	if err != nil {
		return tools.ErrorResult(err.Error())
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

	// Line mode (file_read_lines): read a numbered slice of lines from the file
	// (position is at 0 after the sniff reset). Bounded by lineCount and the byte
	// ceiling.
	if t.lineMode {
		if startLine < 1 {
			startLine = 1
		}
		if lineCount <= 0 {
			lineCount = defaultReadLineCount
		}
		return t.readLines(file, path, startLine, lineCount)
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

	logger.DebugCF("tool", "ReadFileTool execution completed successfully",
		map[string]any{
			"path":       path,
			"bytes_read": len(data),
			"has_more":   hasMore,
		})

	// The status block follows the content (not a header) so the actionable
	// continuation instruction sits at the model's highest-attention position.
	// A hint buried above a large chunk was being ignored, causing models to
	// re-read the same front chunk instead of advancing the offset.
	return tools.NewToolResult(string(data) +
		byteReadStatus(path, filepath.Base(path), offset, readEnd, length, totalSize, hasMore))
}

// byteReadStatus renders the explicit end-of-output status block for a byte-mode
// read, including the exact next call to make. Placed after the content.
func byteReadStatus(path, displayPath string, offset, readEnd, length, totalSize int64, hasMore bool) string {
	var b strings.Builder
	b.WriteString("\n\n=== FILE READ STATUS ===\n")
	fmt.Fprintf(&b, "File: %s\n", displayPath)
	if totalSize >= 0 {
		fmt.Fprintf(&b, "Total size: %d bytes\n", totalSize)
	}
	if totalSize >= 0 && length > 0 {
		totalChunks := (totalSize + length - 1) / length
		if totalChunks < 1 {
			totalChunks = 1
		}
		fmt.Fprintf(&b, "Chunk returned: bytes %d-%d (chunk %d of %d)\n",
			offset, readEnd-1, offset/length+1, totalChunks)
	} else {
		fmt.Fprintf(&b, "Chunk returned: bytes %d-%d\n", offset, readEnd-1)
	}
	if hasMore {
		b.WriteString("Status: TRUNCATED — more content remains\n\n")
		b.WriteString("ACTION REQUIRED:\n")
		b.WriteString("  To continue, call:\n")
		fmt.Fprintf(&b, "  file_read_bytes(path=%q, offset=%d)\n\n", path, readEnd)
		fmt.Fprintf(&b, "Do NOT call file_read_bytes again without offset=%d — you will receive the same chunk.\n", readEnd)
	} else {
		b.WriteString("Status: COMPLETE — END OF FILE, no further content\n")
	}
	b.WriteString("=== END STATUS ===")
	return b.String()
}

// readLines returns a numbered slice of lines [startLine, startLine+lineCount)
// from file (positioned at 0), bounded by the byte ceiling. The output is
// prefixed with each line's 1-based number and a header noting the range and how
// to fetch the next chunk.
func (t *ReadFileTool) readLines(file io.Reader, path string, startLine, lineCount int64) *tools.ToolResult {
	displayPath := filepath.Base(path)
	scanner := bufio.NewScanner(file)
	// Allow long lines up to the byte ceiling (default 64KB buffer is too small).
	scanner.Buffer(make([]byte, 0, 64*1024), int(t.maxSize)+1)

	var b strings.Builder
	var cur, emitted, lastLine int64
	endLine := startLine + lineCount // exclusive
	bytesCapped := false
	moreLines := false

	for scanner.Scan() {
		cur++
		if cur < startLine {
			continue
		}
		if cur >= endLine {
			moreLines = true
			break
		}
		line := scanner.Text()
		// Stop before exceeding the byte ceiling so the result never overflows
		// context; tell the caller to continue from here.
		if int64(b.Len()+len(line))+16 > t.maxSize {
			bytesCapped = true
			moreLines = true
			break
		}
		fmt.Fprintf(&b, "%d: %s\n", cur, line)
		emitted++
		lastLine = cur
	}
	if err := scanner.Err(); err != nil {
		return tools.ErrorResult(fmt.Sprintf("failed to read lines: %v", err))
	}

	if emitted == 0 {
		return tools.NewToolResult(fmt.Sprintf("[file: %s | no lines at start_line=%d (file has %d line(s))]", displayPath, startLine, cur))
	}

	logger.DebugCF("tool", "ReadFileTool line-mode read completed",
		map[string]any{"path": displayPath, "start_line": startLine, "lines": emitted, "more": moreLines})
	return tools.NewToolResult(b.String() +
		lineReadStatus(path, displayPath, startLine, lastLine, bytesCapped || moreLines, bytesCapped))
}

// lineReadStatus renders the explicit end-of-output status block for a line-mode
// read, including the exact next call. Placed after the content (see
// byteReadStatus for why).
func lineReadStatus(path, displayPath string, startLine, lastLine int64, moreFollow, bytesCapped bool) string {
	var b strings.Builder
	b.WriteString("\n\n=== FILE READ STATUS ===\n")
	fmt.Fprintf(&b, "File: %s\n", displayPath)
	fmt.Fprintf(&b, "Lines returned: %d-%d\n", startLine, lastLine)
	if moreFollow {
		if bytesCapped {
			b.WriteString("Status: TRUNCATED at the byte limit — more lines remain\n\n")
		} else {
			b.WriteString("Status: TRUNCATED — more lines remain\n\n")
		}
		b.WriteString("ACTION REQUIRED:\n")
		b.WriteString("  To continue, call:\n")
		fmt.Fprintf(&b, "  file_read_lines(path=%q, start_line=%d)\n\n", path, lastLine+1)
		fmt.Fprintf(&b, "Do NOT call file_read_lines again without start_line=%d — you will receive the same lines.\n", lastLine+1)
	} else {
		b.WriteString("Status: COMPLETE — END OF FILE, no further lines\n")
	}
	b.WriteString("=== END STATUS ===")
	return b.String()
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

// argPresent reports whether the caller supplied a non-nil value for key.
func argPresent(args map[string]any, key string) bool {
	v, ok := args[key]
	return ok && v != nil
}

type WriteFileTool struct {
	sysFs fileSystem
}

func NewWriteFileTool(workspace string, restrict bool, allowPaths ...[]*regexp.Regexp) *WriteFileTool {
	return NewWriteFileToolScoped(workspace, restrict, "", allowPaths...)
}

// NewWriteFileToolScoped constructs a WriteFileTool whose writes are confined to
// <workspace>/<writeSubdir> (reads stay workspace-wide). An empty writeSubdir
// yields the legacy whole-workspace behaviour.
func NewWriteFileToolScoped(workspace string, restrict bool, writeSubdir string, allowPaths ...[]*regexp.Regexp) *WriteFileTool {
	var patterns []*regexp.Regexp
	if len(allowPaths) > 0 {
		patterns = allowPaths[0]
	}
	return &WriteFileTool{sysFs: buildWriteFs(workspace, restrict, writeSubdir, patterns)}
}

func (t *WriteFileTool) Name() string {
	return "file_write"
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

func (t *ListDirTool) Name() string {
	return "file_list"
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
	Remove(path string) error
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

func (h *hostFs) Remove(path string) error {
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("failed to delete file: file not found: %w", err)
		}
		if os.IsPermission(err) {
			return fmt.Errorf("failed to delete file: access denied: %w", err)
		}
		return fmt.Errorf("failed to delete file: %w", err)
	}
	return nil
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

// tmpFileSeq makes atomic-write temp filenames unique across concurrent
// goroutines in this process. os.Getpid()+UnixNano() alone collides when two
// goroutines hit the same nanosecond, which broke concurrent backups under load.
var tmpFileSeq atomic.Uint64

func (r *sandboxFs) WriteFileMode(path string, data []byte, mode os.FileMode) error {
	return r.execute(path, func(root *os.Root, relPath string) error {
		dir := filepath.Dir(relPath)
		if dir != "." && dir != "/" {
			if err := root.MkdirAll(dir, 0o755); err != nil {
				return fmt.Errorf("failed to create parent directories: %w", err)
			}
		}

		// pid+nano for human/cross-process readability; the atomic counter
		// guarantees uniqueness across concurrent goroutines within this process.
		tmpRelPath := fmt.Sprintf(".tmp-%d-%d-%d", os.Getpid(), time.Now().UnixNano(), tmpFileSeq.Add(1))

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

func (r *sandboxFs) Remove(path string) error {
	return r.execute(path, func(root *os.Root, relPath string) error {
		if err := root.Remove(relPath); err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("failed to delete file: file not found: %w", err)
			}
			if os.IsPermission(err) || strings.Contains(err.Error(), "escapes from parent") ||
				strings.Contains(err.Error(), "permission denied") {
				return fmt.Errorf("failed to delete file: access denied: %w", err)
			}
			return fmt.Errorf("failed to delete file: %w", err)
		}
		return nil
	})
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

func (w *whitelistFs) Remove(path string) error {
	if w.matches(path) {
		return w.host.Remove(path)
	}
	return w.sandbox.Remove(path)
}

// buildFs returns the appropriate fileSystem implementation based on restriction
// settings and optional path whitelist patterns.
func buildFs(workspace string, restrict bool, patterns []*regexp.Regexp) fileSystem {
	return withMounts(workspace, buildBaseFs(workspace, restrict, patterns))
}

// buildBaseFs is buildFs without the external-mount layer; buildWriteFs uses it
// so the write-scope sits inside the mount layer (mountFs is always outermost,
// so mount paths bypass the workspace read/write scopes and use their own sandbox).
func buildBaseFs(workspace string, restrict bool, patterns []*regexp.Regexp) fileSystem {
	if !restrict {
		return &hostFs{}
	}
	sandbox := &sandboxFs{workspace: workspace}
	var inner fileSystem = sandbox
	if len(patterns) > 0 {
		inner = &whitelistFs{sandbox: sandbox, patterns: patterns}
	}
	// Optional read-scope: confine agent reads to specific workspace subdirs
	// (e.g. files/ + skills/). Off by default (nil) so direct unit-test
	// construction is unaffected; the provider enables it from config.
	if len(readScopeSubdirs) > 0 && workspace != "" {
		return newReadScopedFs(inner, workspace, readScopeSubdirs, patterns)
	}
	return inner
}

// readScopeSubdirs, when non-empty, confines agent file reads to these workspace
// subdirectories (plus any allow-listed host paths). It is a single process-wide
// read policy, set once at provider construction from
// AgentDefaults.WorkspaceReadSubdirs; empty means legacy workspace-wide reads.
var readScopeSubdirs []string

// SetReadScopeSubdirs installs the workspace read allowlist (e.g. ["files","skills"]).
// Passing nil/empty restores legacy workspace-wide reads.
func SetReadScopeSubdirs(subdirs []string) { readScopeSubdirs = subdirs }

// readScopedFs wraps an inner fileSystem to confine reads to a set of workspace
// subdirectories (readRoots). Host paths matching patterns (e.g. the global
// skills dir, Tools.AllowReadPaths) bypass the check. Writes pass through — the
// write tools layer writeScopedFs on top for write confinement.
type readScopedFs struct {
	inner       fileSystem
	workspace   string
	readRoots   []string // absolute
	subdirNames []string // for the error message
	patterns    []*regexp.Regexp
}

func newReadScopedFs(inner fileSystem, workspace string, subdirs []string, patterns []*regexp.Regexp) *readScopedFs {
	roots := make([]string, 0, len(subdirs))
	for _, s := range subdirs {
		if abs, err := filepath.Abs(filepath.Join(workspace, s)); err == nil {
			roots = append(roots, abs)
		}
	}
	return &readScopedFs{inner: inner, workspace: workspace, readRoots: roots, subdirNames: subdirs, patterns: patterns}
}

func (r *readScopedFs) readAllowed(path string) error {
	for _, p := range r.patterns {
		if p.MatchString(path) {
			return nil
		}
	}
	var absPath string
	if filepath.IsAbs(path) {
		absPath = filepath.Clean(path)
	} else {
		a, err := filepath.Abs(filepath.Join(r.workspace, path))
		if err != nil {
			return fmt.Errorf("failed to resolve file path: %w", err)
		}
		absPath = a
	}
	for _, root := range r.readRoots {
		if absPath == root || isWithinWorkspace(absPath, root) {
			return nil
		}
	}
	return fmt.Errorf("read denied: the agent can only read %s/", strings.Join(r.subdirNames, "/, "))
}

func (r *readScopedFs) ReadFile(path string) ([]byte, error) {
	if err := r.readAllowed(path); err != nil {
		return nil, err
	}
	return r.inner.ReadFile(path)
}

func (r *readScopedFs) ReadDir(path string) ([]os.DirEntry, error) {
	if err := r.readAllowed(path); err != nil {
		return nil, err
	}
	return r.inner.ReadDir(path)
}

func (r *readScopedFs) Open(path string) (fs.File, error) {
	if err := r.readAllowed(path); err != nil {
		return nil, err
	}
	return r.inner.Open(path)
}

func (r *readScopedFs) Stat(path string) (os.FileInfo, error) {
	if err := r.readAllowed(path); err != nil {
		return nil, err
	}
	return r.inner.Stat(path)
}

func (r *readScopedFs) WriteFile(path string, data []byte) error {
	return r.inner.WriteFile(path, data)
}

func (r *readScopedFs) WriteFileMode(path string, data []byte, mode os.FileMode) error {
	return r.inner.WriteFileMode(path, data, mode)
}

func (r *readScopedFs) WriteFileExclMode(path string, data []byte, mode os.FileMode) error {
	return r.inner.WriteFileExclMode(path, data, mode)
}

func (r *readScopedFs) Remove(path string) error {
	return r.inner.Remove(path)
}

// buildWriteFs returns the fileSystem for the write/edit/append/copy tools.
// When restrict is on and writeSubdir is non-empty, writes are confined to
// <workspace>/<writeSubdir> while reads stay workspace-wide; host paths matching
// patterns (Tools.AllowWritePaths) remain writable. When writeSubdir is empty,
// behaviour matches buildFs (legacy: the whole workspace is writable).
func buildWriteFs(workspace string, restrict bool, writeSubdir string, patterns []*regexp.Regexp) fileSystem {
	base := buildBaseFs(workspace, restrict, patterns)
	if !restrict || writeSubdir == "" {
		return withMounts(workspace, base)
	}
	// mountFs stays outermost so writes to a mount (`<name>/...`) are not rejected
	// by the workspace write-scope; non-mount writes still confine to writeSubdir.
	scoped := &writeScopedFs{
		inner:     base,
		workspace: workspace,
		writeRoot: filepath.Join(workspace, writeSubdir),
		patterns:  patterns,
	}
	return withMounts(workspace, scoped)
}

// writeScopedFs wraps an inner fileSystem to allow reads across the whole
// workspace while confining writes (and the directory creation they imply) to
// writeRoot (<workspace>/<subdir>). Host paths matching patterns are exempt so
// Tools.AllowWritePaths keeps working.
type writeScopedFs struct {
	inner     fileSystem
	workspace string
	writeRoot string
	patterns  []*regexp.Regexp
}

// writeAllowed reports whether a write to path is permitted: either the path
// matches a whitelisted host pattern, or it resolves within writeRoot.
func (w *writeScopedFs) writeAllowed(path string) error {
	for _, p := range w.patterns {
		if p.MatchString(path) {
			return nil
		}
	}

	absRoot, err := filepath.Abs(w.writeRoot)
	if err != nil {
		return fmt.Errorf("failed to resolve write root: %w", err)
	}

	var absPath string
	if filepath.IsAbs(path) {
		absPath = filepath.Clean(path)
	} else {
		absPath, err = filepath.Abs(filepath.Join(w.workspace, path))
		if err != nil {
			return fmt.Errorf("failed to resolve file path: %w", err)
		}
	}

	if absPath != absRoot && !isWithinWorkspace(absPath, absRoot) {
		return fmt.Errorf("write denied: outside %s", w.writeRoot)
	}
	return nil
}

func (w *writeScopedFs) ReadFile(path string) ([]byte, error) {
	return w.inner.ReadFile(path)
}

func (w *writeScopedFs) ReadDir(path string) ([]os.DirEntry, error) {
	return w.inner.ReadDir(path)
}

func (w *writeScopedFs) Open(path string) (fs.File, error) {
	return w.inner.Open(path)
}

func (w *writeScopedFs) Stat(path string) (os.FileInfo, error) {
	return w.inner.Stat(path)
}

func (w *writeScopedFs) Remove(path string) error {
	if err := w.writeAllowed(path); err != nil {
		return err
	}
	return w.inner.Remove(path)
}

func (w *writeScopedFs) WriteFile(path string, data []byte) error {
	if err := w.writeAllowed(path); err != nil {
		return err
	}
	return w.inner.WriteFile(path, data)
}

func (w *writeScopedFs) WriteFileMode(path string, data []byte, mode os.FileMode) error {
	if err := w.writeAllowed(path); err != nil {
		return err
	}
	return w.inner.WriteFileMode(path, data, mode)
}

func (w *writeScopedFs) WriteFileExclMode(path string, data []byte, mode os.FileMode) error {
	if err := w.writeAllowed(path); err != nil {
		return err
	}
	return w.inner.WriteFileExclMode(path, data, mode)
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
