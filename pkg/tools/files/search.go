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
	byteSnippetTrailing     = 80      // bytes of context shown after a byte match
)

// SearchFilesTool greps file contents (a file or a directory tree). It has two
// addressing modes that pair with the two read tools so the caller never mixes
// units:
//
//   - line mode  (file_search_lines): reports path + line number, feeding file_read_lines
//   - byte mode  (file_search_bytes): reports path + byte start/end, feeding file_read_bytes
type SearchFilesTool struct {
	sysFs    fileSystem
	byteMode bool
}

// NewSearchLinesTool builds the line-addressed search tool (file_search_lines).
func NewSearchLinesTool(workspace string, restrict bool, allowPaths ...[]*regexp.Regexp) *SearchFilesTool {
	return newSearchTool(false, workspace, restrict, allowPaths...)
}

// NewSearchBytesTool builds the byte-addressed search tool (file_search_bytes).
func NewSearchBytesTool(workspace string, restrict bool, allowPaths ...[]*regexp.Regexp) *SearchFilesTool {
	return newSearchTool(true, workspace, restrict, allowPaths...)
}

func newSearchTool(byteMode bool, workspace string, restrict bool, allowPaths ...[]*regexp.Regexp) *SearchFilesTool {
	var patterns []*regexp.Regexp
	if len(allowPaths) > 0 {
		patterns = allowPaths[0]
	}
	return &SearchFilesTool{sysFs: buildFs(workspace, restrict, patterns), byteMode: byteMode}
}

func (t *SearchFilesTool) Name() string {
	if t.byteMode {
		return "file_search_bytes"
	}
	return "file_search_lines"
}

func (t *SearchFilesTool) Description() string {
	if t.byteMode {
		return "Search file contents and return each match's BYTE position (start–end) and a snippet — like grep with byte offsets. " +
			"Pass a match's start byte as `offset` to `file_read_bytes` to read from there. " +
			"Use this when you page files by bytes; for human-readable line references use `file_search_lines` instead. " +
			"Searches a single file or a directory tree (recursively). Literal, case-insensitive match by default; set regex=true for a regular expression."
	}
	return "Search file contents and return matching lines with their path and LINE NUMBER — like grep. " +
		"Pass a match's line number as `start_line` to `file_read_lines` to read from there. " +
		"Use it to locate a section (e.g. a chapter heading) before reading or editing. " +
		"Searches a single file or a directory tree (recursively). Literal, case-insensitive match by default; set regex=true for a regular expression. " +
		"(For byte offsets instead of line numbers, use `file_search_bytes`.)"
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
				"description": "Maximum matches to return (default 50, max 200).",
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

	// One compiled pattern drives both modes: literal queries become a
	// case-insensitive quoted regex; regex queries compile as written.
	var re *regexp.Regexp
	if useRegex {
		re, err = regexp.Compile(query)
		if err != nil {
			return tools.ErrorResult(fmt.Sprintf("invalid regex: %v", err))
		}
	} else {
		re, err = regexp.Compile("(?i)" + regexp.QuoteMeta(query))
		if err != nil {
			return tools.ErrorResult(fmt.Sprintf("invalid query: %v", err))
		}
	}

	info, statErr := t.sysFs.Stat(root)
	if statErr != nil {
		return tools.ErrorResult(statErr.Error())
	}

	var (
		out       []string
		scanned   int
		truncated bool
	)
	visit := func(p string) bool { // returns false to stop walking
		scanned++
		hits := t.searchFile(p, re, int(maxResults)-len(out))
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
	unit := "line numbers"
	cont := "Read from a match with file_read_lines(start_line=<line>)."
	if t.byteMode {
		unit = "byte offsets"
		cont = "Read from a match with file_read_bytes(offset=<start>)."
	}
	header := fmt.Sprintf("%d match(es) for %q in %s (%s):", len(out), query, root, unit)
	if truncated {
		header += fmt.Sprintf("\n[Showing the first %d — narrow the query or search a subdirectory for more.]", len(out))
	}
	header += "\n" + cont
	logger.DebugCF("tool", "SearchFilesTool completed",
		map[string]any{"query": query, "root": root, "matches": len(out), "scanned": scanned, "byte_mode": t.byteMode})
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

// searchFile returns up to limit matches from one file, formatted for the active
// mode. Binary and oversized files are skipped.
func (t *SearchFilesTool) searchFile(p string, re *regexp.Regexp, limit int) []string {
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
	sniff := data
	if len(sniff) > 512 {
		sniff = sniff[:512]
	}
	if bytes.IndexByte(sniff, 0) >= 0 {
		return nil
	}

	if t.byteMode {
		return searchFileBytes(p, data, re, limit)
	}
	return searchFileLines(p, data, re, limit)
}

// searchFileLines reports "path:line: text" matches.
func searchFileLines(p string, data []byte, re *regexp.Regexp, limit int) []string {
	var hits []string
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		if !re.MatchString(line) {
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

// searchFileBytes reports "path: bytes start-end: snippet" matches, where start
// is the byte offset to hand to file_read_bytes.
func searchFileBytes(p string, data []byte, re *regexp.Regexp, limit int) []string {
	locs := re.FindAllIndex(data, limit)
	if locs == nil {
		return nil
	}
	hits := make([]string, 0, len(locs))
	for _, loc := range locs {
		start, end := loc[0], loc[1]
		snipEnd := end + byteSnippetTrailing
		if snipEnd > len(data) {
			snipEnd = len(data)
		}
		snippet := strings.TrimSpace(strings.ReplaceAll(string(data[start:snipEnd]), "\n", " "))
		if len(snippet) > maxSearchLineWidth {
			snippet = snippet[:maxSearchLineWidth] + "…"
		}
		hits = append(hits, fmt.Sprintf("%s: bytes %d-%d: %s", p, start, end, snippet))
	}
	return hits
}
