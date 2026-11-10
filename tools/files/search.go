package files

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/go-git/go-git/v5/plumbing/format/gitignore"

	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/tools"
)

const (
	defaultSearchMaxResults = 50
	maxSearchMaxResults     = 200
	maxSearchFileBytes      = 1 << 20 // skip files larger than 1 MB
	maxSearchFilesScanned   = 5000    // cap on files examined per search
	maxSearchLineWidth      = 240     // trim long matching lines for display
	byteSnippetTrailing     = 80      // bytes of context shown after a byte match
	maxSearchContextLines   = 10      // cap on file_search_lines' context
)

// Output modes of file_search_lines.
const (
	searchOutputLines = "lines" // matching lines (default)
	searchOutputFiles = "files" // paths of the files that match
	searchOutputCount = "count" // matching lines per file
)

// gitignoreFile is the file whose patterns file_search_lines honours in the
// searched tree.
const gitignoreFile = ".gitignore"

// SearchFilesTool greps file contents (a file or a directory tree). It has two
// addressing modes that pair with the two read tools so the caller never mixes
// units:
//
//   - line mode  (file_search_lines): reports path + line number, feeding file_read_lines
//   - byte mode  (file_search_bytes): reports path + byte start/end, feeding file_read_bytes
//
// Line mode also takes include, context and output, and skips what the
// .gitignore files in the searched tree ignore; byte mode has none of these.
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
	return "Find text in files: returns matching lines with their path and line number, like grep. " +
		"Then read around a match with file_read_lines (start_line = the line number). " +
		"Searches one file or a folder and everything below it, skipping files .gitignore ignores. " +
		"Literal, case-insensitive by default; regex=true for a regular expression. " +
		"To find files by name, use file_glob."
}

func (t *SearchFilesTool) Parameters() map[string]any {
	props := map[string]any{
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
	}
	if !t.byteMode {
		props["max_results"] = map[string]any{
			"type":        "integer",
			"description": "Maximum matches to return, or files with output=files or count (default 50, max 200).",
			"default":     defaultSearchMaxResults,
		}
		props["include"] = map[string]any{
			"type": "string",
			"description": "Only search files whose path matches this glob, e.g. *.go or src/**/*.ts. " +
				"A pattern without / matches the file name in any folder.",
		}
		props["context"] = map[string]any{
			"type":        "integer",
			"description": fmt.Sprintf("Lines to show before and after each match (default 0, max %d).", maxSearchContextLines),
			"default":     0,
		}
		props["output"] = map[string]any{
			"type": "string",
			"enum": []string{searchOutputLines, searchOutputFiles, searchOutputCount},
			"description": "lines (default): the matching lines; files: only the paths of files that match; " +
				"count: the number of matching lines in each file.",
			"default": searchOutputLines,
		}
	}
	return map[string]any{
		"type":       "object",
		"properties": props,
		"required":   []string{"query"},
	}
}

// lineOptions are file_search_lines' own arguments.
type lineOptions struct {
	include string // doublestar glob on the file path; "" = every file
	context int    // lines before and after each match
	output  string // searchOutputLines, searchOutputFiles or searchOutputCount
}

// parseLineOptions reads include, context and output.
func parseLineOptions(args map[string]any) (lineOptions, error) {
	opts := lineOptions{output: searchOutputLines}
	if v, ok := args["include"].(string); ok {
		opts.include = strings.TrimSpace(v)
		if opts.include != "" && !doublestar.ValidatePattern(opts.include) {
			return opts, fmt.Errorf("include is not a valid glob pattern: %s", opts.include)
		}
	}
	n, err := getInt64Arg(args, "context", 0)
	if err != nil {
		return opts, err
	}
	opts.context = int(max(0, min(n, maxSearchContextLines)))
	if v, ok := args["output"].(string); ok && strings.TrimSpace(v) != "" {
		switch v = strings.ToLower(strings.TrimSpace(v)); v {
		case searchOutputLines, searchOutputFiles, searchOutputCount:
			opts.output = v
		default:
			return opts, fmt.Errorf("output must be lines, files or count, not %q", v)
		}
	}
	return opts, nil
}

// included reports whether the file at rel (slash-separated, relative to the
// searched folder) passes the include glob. A pattern without "/" is matched
// against the file name alone, so "*.go" finds Go files in every folder.
func (o lineOptions) included(rel string) bool {
	if o.include == "" {
		return true
	}
	target := rel
	if !strings.Contains(o.include, "/") {
		target = path.Base(rel)
	}
	ok, err := doublestar.Match(o.include, target)
	return err == nil && ok
}

func (t *SearchFilesTool) Execute(_ context.Context, args map[string]any) *tools.ToolResult {
	query, ok := args["query"].(string)
	if !ok || strings.TrimSpace(query) == "" {
		return tools.ErrorResult("query is required")
	}
	root, ok := args["path"].(string)
	if !ok || strings.TrimSpace(root) == "" {
		root = "."
	}
	useRegex := getBoolArg(args, "regex", false)
	maxResults, err := getInt64Arg(args, "max_results", defaultSearchMaxResults)
	if err != nil {
		return errResult(err)
	}
	if maxResults <= 0 || maxResults > maxSearchMaxResults {
		maxResults = maxSearchMaxResults
	}
	var opts lineOptions
	if !t.byteMode {
		if opts, err = parseLineOptions(args); err != nil {
			return errResult(err)
		}
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
		return errResult(statErr)
	}
	if !t.byteMode {
		return t.searchLines(query, root, info.IsDir(), re, int(maxResults), opts)
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
	header := fmt.Sprintf("%d match(es) for %q in %s (byte offsets):", len(out), query, root)
	if truncated {
		header += fmt.Sprintf("\n[Showing the first %d — narrow the query or search a subdirectory for more.]", len(out))
	}
	header += "\nRead from a match with file_read_bytes(offset=<start>)."
	logger.DebugCF("tool", "SearchFilesTool completed",
		map[string]any{"query": query, "root": root, "matches": len(out), "scanned": scanned, "byte_mode": t.byteMode})
	return tools.NewToolResult(header + "\n\n" + strings.Join(out, "\n"))
}

// searchLines is file_search_lines: the matches, files or counts under root
// (a folder when isDir), at most limit of them (matches in lines mode, files
// otherwise).
func (t *SearchFilesTool) searchLines(query, root string, isDir bool, re *regexp.Regexp, limit int, opts lineOptions) *tools.ToolResult {
	var (
		out       []string // one entry per match block, file or count
		found     int      // matches (lines mode) or files (files/count)
		scanned   int
		truncated bool
	)
	visit := func(p, rel string) bool { // returns false to stop walking
		scanned++ // every file walked counts, so a narrow include still stops
		if !opts.included(rel) {
			return scanned < maxSearchFilesScanned
		}
		data := t.searchableData(p)
		if data == nil {
			return scanned < maxSearchFilesScanned
		}
		switch opts.output {
		case searchOutputFiles:
			if len(matchingLines(data, re, 1)) > 0 {
				out = append(out, p)
				found++
			}
		case searchOutputCount:
			if n := len(matchingLines(data, re, -1)); n > 0 {
				out = append(out, fmt.Sprintf("%s: %d", p, n))
				found++
			}
		default:
			hits, n := formatLineMatches(p, data, re, limit-found, opts.context)
			out = append(out, hits...)
			found += n
		}
		if found >= limit {
			truncated = true
			return false
		}
		return scanned < maxSearchFilesScanned
	}

	if isDir {
		t.walkLines(root, nil, nil, visit)
	} else {
		// A single file is matched by the path as given (by its name for a
		// pattern without "/").
		visit(root, root)
	}

	if found == 0 {
		return tools.NewToolResult(fmt.Sprintf("No matches for %q in %s.", query, root))
	}
	var header string
	switch opts.output {
	case searchOutputFiles:
		header = fmt.Sprintf("%d file(s) match %q in %s:", found, query, root)
	case searchOutputCount:
		header = fmt.Sprintf("%d file(s) match %q in %s (matching lines per file):", found, query, root)
	default:
		header = fmt.Sprintf("%d match(es) for %q in %s (line numbers):", found, query, root)
	}
	if truncated {
		header += fmt.Sprintf("\n[Showing the first %d — narrow the query or search a subdirectory for more.]", found)
	}
	sep := "\n"
	switch opts.output {
	case searchOutputLines:
		header += "\nRead from a match with file_read_lines(start_line=<line>)."
		if opts.context > 0 {
			header += "\nMatching lines are path:line: text, the lines around them path-line- text."
			sep = "\n--\n"
		}
	default:
		header += "\nSearch one file with output=lines to see its matching lines."
	}
	logger.DebugCF("tool", "SearchFilesTool completed",
		map[string]any{"query": query, "root": root, "found": found, "scanned": scanned, "output": opts.output})
	return tools.NewToolResult(header + "\n\n" + strings.Join(out, sep))
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

// walkLines is walk for file_search_lines: it also passes each file's path
// relative to the searched folder (slash-separated) and skips what the
// .gitignore files in the searched tree ignore. rel is dir's path below the
// searched folder; ignores holds the patterns read so far, each scoped to the
// folder its file is in. It returns false once visit asked to stop.
func (t *SearchFilesTool) walkLines(dir string, rel []string, ignores []gitignore.Pattern, visit func(p, rel string) bool) bool {
	entries, err := t.sysFs.ReadDir(dir)
	if err != nil {
		return true
	}
	if data, err := t.sysFs.ReadFile(path.Join(dir, gitignoreFile)); err == nil {
		ignores = append(slices.Clip(ignores), parseGitignore(data, rel)...)
	}
	matcher := gitignore.NewMatcher(ignores)
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue // skip dotfiles/dirs (.git, etc.)
		}
		childRel := append(slices.Clip(rel), name)
		if len(ignores) > 0 && matcher.Match(childRel, e.IsDir()) {
			continue
		}
		child := path.Join(dir, name)
		if e.IsDir() {
			if !t.walkLines(child, childRel, ignores, visit) {
				return false
			}
			continue
		}
		if !visit(child, strings.Join(childRel, "/")) {
			return false
		}
	}
	return true
}

// parseGitignore reads the patterns of one .gitignore file in the folder at
// domain (relative to the searched folder): blank lines and comments are
// left out, as git does.
func parseGitignore(data []byte, domain []string) []gitignore.Pattern {
	var out []gitignore.Pattern
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" {
			continue
		}
		out = append(out, gitignore.ParsePattern(line, domain))
	}
	return out
}

// searchableData returns a file's content, or nil for a file the search skips:
// one it cannot read, larger than maxSearchFileBytes, or binary.
func (t *SearchFilesTool) searchableData(p string) []byte {
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
	return data
}

// searchFile returns up to limit matches from one file, formatted for the active
// mode. Binary and oversized files are skipped.
func (t *SearchFilesTool) searchFile(p string, re *regexp.Regexp, limit int) []string {
	if limit <= 0 {
		return nil
	}
	data := t.searchableData(p)
	if data == nil {
		return nil
	}
	if t.byteMode {
		return searchFileBytes(p, data, re, limit)
	}
	return searchFileLines(p, data, re, limit)
}

// fileLines splits a file's content into its lines (without line endings).
func fileLines(data []byte) []string {
	var lines []string
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines
}

// matchingLines returns the 1-based numbers of the lines re matches, at most
// limit of them (limit < 0: all).
func matchingLines(data []byte, re *regexp.Regexp, limit int) []int {
	var out []int
	for i, line := range fileLines(data) {
		if limit >= 0 && len(out) >= limit {
			break
		}
		if re.MatchString(line) {
			out = append(out, i+1)
		}
	}
	return out
}

// clipLine cuts a line to maxSearchLineWidth for display.
func clipLine(s string) string {
	if len(s) > maxSearchLineWidth {
		return s[:maxSearchLineWidth] + "…"
	}
	return s
}

// formatLineMatches reports up to limit matches of one file and how many it
// reported. Without context each match is "path:line: text" (today's format,
// one entry per match); with context, each run of nearby matches is one entry
// whose matching lines are "path:line: text" and whose other lines are
// "path-line- text", indentation kept.
func formatLineMatches(p string, data []byte, re *regexp.Regexp, limit, context int) ([]string, int) {
	if limit <= 0 {
		return nil, 0
	}
	if context == 0 {
		hits := searchFileLines(p, data, re, limit)
		return hits, len(hits)
	}
	lines := fileLines(data)
	var matches []int // 0-based
	for i, line := range lines {
		if len(matches) >= limit {
			break
		}
		if re.MatchString(line) {
			matches = append(matches, i)
		}
	}
	var (
		blocks []string
		b      strings.Builder
		end    = -1 // last line written to the current block
	)
	for _, m := range matches {
		from := max(0, m-context)
		to := min(len(lines)-1, m+context)
		if end >= 0 && from > end+1 {
			blocks = append(blocks, strings.TrimSuffix(b.String(), "\n"))
			b.Reset()
		}
		for i := max(from, end+1); i <= to; i++ {
			text := clipLine(strings.TrimRight(lines[i], " \t"))
			// Any matching line is marked as one, also one past the limit
			// that falls inside a block.
			if re.MatchString(lines[i]) {
				fmt.Fprintf(&b, "%s:%d: %s\n", p, i+1, text)
			} else {
				fmt.Fprintf(&b, "%s-%d- %s\n", p, i+1, text)
			}
		}
		end = max(end, to)
	}
	if b.Len() > 0 {
		blocks = append(blocks, strings.TrimSuffix(b.String(), "\n"))
	}
	return blocks, len(matches)
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
		snipEnd := min(end+byteSnippetTrailing, len(data))
		snippet := strings.TrimSpace(strings.ReplaceAll(string(data[start:snipEnd]), "\n", " "))
		if len(snippet) > maxSearchLineWidth {
			snippet = snippet[:maxSearchLineWidth] + "…"
		}
		hits = append(hits, fmt.Sprintf("%s: bytes %d-%d: %s", p, start, end, snippet))
	}
	return hits
}
