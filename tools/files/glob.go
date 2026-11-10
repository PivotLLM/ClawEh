package files

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/tools"
)

const (
	maxGlobResults        = 200   // file paths returned per call
	maxGlobEntriesScanned = 50000 // cap on folder entries examined per call
)

// GlobTool finds files by name pattern (file_glob). It reads through the same
// file-system layers as the other file-reading tools, so it reaches exactly
// what they reach: the workspace read scope and the allowed folders.
type GlobTool struct {
	sysFs fileSystem
}

// NewGlobTool builds file_glob.
func NewGlobTool(workspace string, restrict bool, allowPaths ...[]*regexp.Regexp) *GlobTool {
	var patterns []*regexp.Regexp
	if len(allowPaths) > 0 {
		patterns = allowPaths[0]
	}
	return &GlobTool{sysFs: buildFs(workspace, restrict, patterns)}
}

func (t *GlobTool) Name() string { return "file_glob" }

func (t *GlobTool) Description() string {
	return fmt.Sprintf("Find files by name pattern, e.g. **/*.go or src/**/test_*.py. "+
		"Returns matching file paths, newest first, at most %d. "+
		"Folders whose name starts with a dot (.git) are skipped. "+
		"To find text inside files, use file_search_lines.", maxGlobResults)
}

func (t *GlobTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern": map[string]any{
				"type": "string",
				"description": "Glob matched against each file's path below `path`: * matches within one folder, " +
					"** any number of folders (**/*.go finds Go files everywhere), ? one character, {a,b} either.",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "Folder to search under, relative to the workspace or absolute. Defaults to the workspace.",
				"default":     ".",
			},
		},
		"required": []string{"pattern"},
	}
}

// globMatch is one file the pattern matched.
type globMatch struct {
	path  string
	mtime time.Time
}

func (t *GlobTool) Execute(_ context.Context, args map[string]any) *tools.ToolResult {
	pattern, ok := args["pattern"].(string)
	pattern = strings.TrimSpace(pattern)
	if !ok || pattern == "" {
		return tools.ErrorResult("pattern is required")
	}
	if !doublestar.ValidatePattern(pattern) {
		return tools.ErrorResult("pattern is not a valid glob pattern: " + pattern)
	}
	root, ok := args["path"].(string)
	if !ok || strings.TrimSpace(root) == "" {
		root = "."
	}
	info, err := t.sysFs.Stat(root)
	if err != nil {
		return errResult(err)
	}
	if !info.IsDir() {
		return tools.ErrorResult(root + " is not a folder")
	}

	var (
		matches []globMatch
		scanned int
		stopped bool
	)
	var walk func(dir, rel string)
	walk = func(dir, rel string) {
		entries, err := t.sysFs.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if scanned >= maxGlobEntriesScanned {
				stopped = true
				return
			}
			scanned++
			name := e.Name()
			childRel := name
			if rel != "" {
				childRel = rel + "/" + name
			}
			child := path.Join(dir, name)
			if e.IsDir() {
				if !strings.HasPrefix(name, ".") { // skip .git and other dot-folders
					walk(child, childRel)
				}
				continue
			}
			if name == global.FolderMarkerFile {
				continue
			}
			if ok, err := doublestar.Match(pattern, childRel); err != nil || !ok {
				continue
			}
			// Stat through the same layers: a link that leads somewhere the
			// agent may not read, or to a folder, is not a match.
			fi, err := t.sysFs.Stat(child)
			if err != nil || fi.IsDir() {
				continue
			}
			matches = append(matches, globMatch{path: child, mtime: fi.ModTime()})
		}
	}
	walk(root, "")

	if len(matches) == 0 {
		msg := fmt.Sprintf("No files match %q in %s.", pattern, root)
		if stopped {
			msg += fmt.Sprintf(" [Stopped after %d entries — search a smaller folder.]", maxGlobEntriesScanned)
		}
		return tools.NewToolResult(msg)
	}
	slices.SortFunc(matches, func(a, b globMatch) int {
		if c := b.mtime.Compare(a.mtime); c != 0 {
			return c
		}
		return strings.Compare(a.path, b.path)
	})
	total := len(matches)
	header := fmt.Sprintf("%d file(s) match %q in %s (newest first):", total, pattern, root)
	if total > maxGlobResults {
		matches = matches[:maxGlobResults]
		header += fmt.Sprintf("\n[Showing the %d newest — narrow the pattern or the path for more.]", maxGlobResults)
	}
	if stopped {
		header += fmt.Sprintf("\n[Stopped after %d entries — search a smaller folder for the rest.]", maxGlobEntriesScanned)
	}
	paths := make([]string, len(matches))
	for i, m := range matches {
		paths[i] = m.path
	}
	logger.DebugCF("tool", "GlobTool completed",
		map[string]any{"pattern": pattern, "root": root, "matches": total, "scanned": scanned})
	return tools.NewToolResult(header + "\n\n" + strings.Join(paths, "\n"))
}
