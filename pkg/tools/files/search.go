package files

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/PivotLLM/ClawEh/pkg/logger"
	"github.com/PivotLLM/ClawEh/pkg/tools"
)

const (
	defaultSearchMaxResults = 50
	maxSearchMaxResults     = 200
	maxSearchFileBytes      = 1 << 20 // skip files larger than 1 MB
	maxSearchFilesScanned   = 5000    // cap on files examined per search
	maxSearchLineWidth      = 240     // trim long matching lines for display
)

// SearchFilesTool greps file contents (a file or a directory tree) and returns
// matching lines with their path and line number, so the agent can locate a
// section without paging through whole files.
type SearchFilesTool struct {
	sysFs fileSystem
}

// NewSearchFilesTool builds a search tool over the same read sandbox as
// NewReadFileTool.
func NewSearchFilesTool(workspace string, restrict bool, allowPaths ...[]*regexp.Regexp) *SearchFilesTool {
	var patterns []*regexp.Regexp
	if len(allowPaths) > 0 {
		patterns = allowPaths[0]
	}
	return &SearchFilesTool{sysFs: buildFs(workspace, restrict, patterns)}
}

func (t *SearchFilesTool) Name() string { return "file_search" }

func (t *SearchFilesTool) Description() string {
	return "Search file contents for a query and return matching lines with their path and line number — like grep. " +
		"Use it to locate a section (e.g. a chapter heading) before reading or editing, instead of paging through a whole file. " +
		"Searches a single file or a directory tree (recursively). Literal, case-insensitive match by default; set regex=true to use a regular expression."
}

func (t *SearchFilesTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "Text to find (literal substring, case-insensitive) or a regular expression when regex=true.",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "File or directory to search (directories are searched recursively). Defaults to the workspace root.",
				"default":     ".",
			},
			"regex": map[string]any{
				"type":        "boolean",
				"description": "Treat query as a regular expression (case-sensitive as written). Default false.",
				"default":     false,
			},
			"max_results": map[string]any{
				"type":        "integer",
				"description": "Maximum matching lines to return (default 50, max 200).",
				"default":     defaultSearchMaxResults,
			},
		},
		"required": []string{"query"},
	}
}

func (t *SearchFilesTool) Execute(_ context.Context, args map[string]any) *tools.ToolResult {
	query, _ := args["query"].(string)
	if strings.TrimSpace(query) == "" {
		return tools.ErrorResult("query is required")
	}
	root, _ := args["path"].(string)
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	useRegex, _ := args["regex"].(bool)
	maxResults, err := getInt64Arg(args, "max_results", defaultSearchMaxResults)
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	if maxResults <= 0 || maxResults > maxSearchMaxResults {
		maxResults = maxSearchMaxResults
	}

	var matcher func(string) bool
	if useRegex {
		re, cerr := regexp.Compile(query)
		if cerr != nil {
			return tools.ErrorResult(fmt.Sprintf("invalid regex: %v", cerr))
		}
		matcher = re.MatchString
	} else {
		needle := strings.ToLower(query)
		matcher = func(line string) bool { return strings.Contains(strings.ToLower(line), needle) }
	}

	info, statErr := t.sysFs.Stat(root)
	if statErr != nil {
		return tools.ErrorResult(statErr.Error())
	}

	var (
		out      []string
		scanned  int
		truncated bool
	)
	visit := func(p string) bool { // returns false to stop walking
		scanned++
		hits := t.searchFile(p, matcher, int(maxResults)-len(out))
		out = append(out, hits...)
		if len(out) >= int(maxResults) {
			truncated = true
			return false
		}
		return scanned < maxSearchFilesScanned
	}

	if info.IsDir() {
		t.walk(root, visit)
	} else {
		visit(root)
	}

	if len(out) == 0 {
		return tools.NewToolResult(fmt.Sprintf("No matches for %q in %s.", query, root))
	}
	header := fmt.Sprintf("%d match(es) for %q in %s:", len(out), query, root)
	if truncated {
		header += fmt.Sprintf("\n[Showing the first %d — narrow the query or search a subdirectory for more.]", len(out))
	}
	logger.DebugCF("tool", "SearchFilesTool completed",
		map[string]any{"query": query, "root": root, "matches": len(out), "scanned": scanned})
	return tools.NewToolResult(header + "\n\n" + strings.Join(out, "\n"))
}

// walk recursively visits files under dir (depth-first), calling visit(path) for
// each regular file. visit returns false to stop the whole walk.
func (t *SearchFilesTool) walk(dir string, visit func(string) bool) {
	entries, err := t.sysFs.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue // skip dotfiles/dirs (.git, etc.)
		}
		child := path.Join(dir, name)
		if e.IsDir() {
			t.walk(child, visit)
			continue
		}
		if !visit(child) {
			return
		}
	}
}

// searchFile returns up to limit "path:line: text" matches from one file. Binary
// and oversized files are skipped.
func (t *SearchFilesTool) searchFile(p string, matcher func(string) bool, limit int) []string {
	if limit <= 0 {
		return nil
	}
	if info, err := t.sysFs.Stat(p); err == nil && info.Size() > maxSearchFileBytes {
		return nil
	}
	data, err := t.sysFs.ReadFile(p)
	if err != nil {
		return nil
	}
	// Skip binary content.
	if sniff := data; len(sniff) > 512 {
		sniff = sniff[:512]
		if bytes.IndexByte(sniff, 0) >= 0 {
			return nil
		}
	} else if bytes.IndexByte(sniff, 0) >= 0 {
		return nil
	}

	var hits []string
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		if !matcher(line) {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if len(trimmed) > maxSearchLineWidth {
			trimmed = trimmed[:maxSearchLineWidth] + "…"
		}
		hits = append(hits, fmt.Sprintf("%s:%d: %s", p, lineNo, trimmed))
		if len(hits) >= limit {
			break
		}
	}
	return hits
}
