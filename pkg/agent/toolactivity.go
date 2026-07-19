package agent

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/PivotLLM/ClawEh/pkg/providers"
)

// toolActivitySummary renders a compact, privacy-safe one-line breadcrumb for a
// tool call — e.g. "🔧 file_read 1–250 outline.md" — for the /tools follow-along
// feature. It exposes ONLY structural metadata (tool name, file basename,
// line/byte ranges, URL host); it NEVER includes file or memory contents, edit
// text, write bodies, or search-query strings, which tool args routinely carry
// (the dispatch path deliberately keeps those out of logs, too). Returns "" only
// for an empty tool name.
func toolActivitySummary(name string, args map[string]any) string {
	name = stripToolNamespace(name)
	if name == "" {
		return ""
	}
	const prefix = "🔧 "

	// Always show the ACTUAL (namespace-stripped) tool name, backticked so channel
	// markdown renderers keep its underscores literal (Telegram's converter would
	// otherwise italicise a _paired_ underscore and drop it). Known tools append a
	// compact, privacy-safe detail (line/byte range, file basename, URL host) —
	// never file or memory contents, edit text, or query strings.
	head := prefix + code(name)

	switch name {
	case "file_read_lines":
		if base := toolArgBase(args, "path"); base != "" {
			return head + " " + readLineRange(args) + " " + code(base)
		}
	case "file_edit_lines", "file_delete_lines", "file_insert_lines":
		if base := toolArgBase(args, "path"); base != "" {
			if r := editLineRange(args); r != "" {
				return head + " " + r + " " + code(base)
			}
			return head + " " + code(base)
		}
	case "file_read_bytes", "file_edit", "file_write", "file_append",
		"file_delete", "file_search_lines", "file_search_bytes":
		if base := toolArgBase(args, "path"); base != "" {
			return head + " " + code(base)
		}
	case "file_list":
		base := toolArgBase(args, "path")
		if base == "" {
			base = "."
		}
		return head + " " + code(base)
	case "file_move", "file_copy":
		src := toolArgBase(args, "source_path")
		dst := toolArgBase(args, "destination_path")
		if src != "" || dst != "" {
			return head + " " + code(src) + " → " + code(dst)
		}
	case "web_fetch":
		if h := urlHost(toolArgStr(args, "url")); h != "" {
			return head + " " + code(h)
		}
	}
	// Unknown/MCP tool: append a short, privacy-safe summary of its scalar args
	// (e.g. "get_tool_details wxca_city_get", "metar_get CYOW") so the user gets
	// basic detail without leaking contents or secrets.
	if detail := toolArgDetail(args); detail != "" {
		return head + " " + detail
	}
	return head
}

const (
	maxDetailArgs     = 3  // most breadcrumbs need only the primary identifier(s)
	maxDetailValueLen = 40 // longer string values are likely content, not an identifier
)

// sensitiveArgKeyParts are case-insensitive substrings of arg keys whose values
// must never appear in a breadcrumb — credentials and content bodies.
var sensitiveArgKeyParts = []string{
	"pass", "secret", "token", "cred", "apikey", "api_key", "auth",
	"content", "body", "old_text", "new_text", "replace", "html", "markdown", "payload",
}

// toolArgDetail renders up to maxDetailArgs short scalar arg VALUES (string/
// number/bool, in stable key order) for the generic breadcrumb, each backticked.
// Secret/content-bearing keys are skipped and over-long strings are dropped, so
// credentials and bodies never leak.
func toolArgDetail(args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, 0, maxDetailArgs)
	for _, k := range keys {
		if len(parts) >= maxDetailArgs {
			break
		}
		if isSensitiveArgKey(k) {
			continue
		}
		switch v := args[k].(type) {
		case string:
			s := strings.TrimSpace(v)
			if s == "" || len([]rune(s)) > maxDetailValueLen {
				continue
			}
			parts = append(parts, code(s))
		case float64:
			parts = append(parts, code(trimFloat(v)))
		case bool:
			parts = append(parts, code(strconv.FormatBool(v)))
		}
	}
	return strings.Join(parts, " ")
}

func isSensitiveArgKey(key string) bool {
	k := strings.ToLower(key)
	for _, p := range sensitiveArgKeyParts {
		if strings.Contains(k, p) {
			return true
		}
	}
	return false
}

// trimFloat renders a JSON number without a trailing ".0" for whole values.
func trimFloat(f float64) string {
	if f == math.Trunc(f) && !math.IsInf(f, 0) {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// code wraps an identifier in backticks so channel markdown renderers treat it as
// literal inline code — protecting underscores and other markdown-significant
// characters in tool/file names from being interpreted as formatting.
func code(s string) string {
	return "`" + s + "`"
}

// toolCallBreadcrumb extracts the (name, args) from a stored ToolCall and renders
// its activity summary. Returns "" when there is nothing to show.
func toolCallBreadcrumb(tc providers.ToolCall) string {
	name := tc.Name
	if name == "" && tc.Function != nil {
		name = tc.Function.Name
	}
	args := tc.Arguments
	if args == nil && tc.Function != nil && strings.TrimSpace(tc.Function.Arguments) != "" {
		_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
	}
	return toolActivitySummary(name, args)
}

// stripToolNamespace removes an "mcp__server__" prefix so a namespaced tool shows
// its bare name.
func stripToolNamespace(name string) string {
	if i := strings.LastIndex(name, "__"); i >= 0 {
		return name[i+2:]
	}
	return name
}

func toolArgStr(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	s, _ := args[key].(string)
	return strings.TrimSpace(s)
}

// toolArgBase returns the basename of a path-valued arg, or "" when absent.
func toolArgBase(args map[string]any, key string) string {
	v := toolArgStr(args, key)
	if v == "" {
		return ""
	}
	return filepath.Base(v)
}

// toolArgInt coerces a JSON-decoded numeric arg (float64 by default) to int; a
// non-numeric or missing arg yields (0, false).
func toolArgInt(args map[string]any, key string) (int, bool) {
	if args == nil {
		return 0, false
	}
	switch n := args[key].(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	case json.Number:
		i, err := n.Int64()
		return int(i), err == nil
	case string:
		i, err := strconv.Atoi(strings.TrimSpace(n))
		return i, err == nil
	}
	return 0, false
}

// defaultReadLineCount mirrors pkg/tools/files' default line_count for
// file_read_lines, used so the breadcrumb shows the full requested range even
// when the model omits line_count (kept in sync manually; display-only).
const defaultReadLineCount = 250

// readLineRange renders a file_read_lines slice as "start–end". When line_count
// is omitted the tool's default applies, so the breadcrumb shows the full range
// the model is reading (e.g. start 766 → "766–1015"), not just the start.
func readLineRange(args map[string]any) string {
	start, ok := toolArgInt(args, "start_line")
	if !ok || start <= 0 {
		start = 1
	}
	count, ok := toolArgInt(args, "line_count")
	if !ok || count <= 0 {
		count = defaultReadLineCount
	}
	return fmt.Sprintf("%d–%d", start, start+count-1)
}

// editLineRange renders a range-edit as "start–end" or "start" (or "" when no
// numeric start is present, e.g. an insert keyed only by after_line).
func editLineRange(args map[string]any) string {
	start, ok := toolArgInt(args, "start")
	if !ok {
		if after, ok := toolArgInt(args, "after_line"); ok {
			return "@" + strconv.Itoa(after)
		}
		return ""
	}
	if end, ok := toolArgInt(args, "end"); ok && end >= start {
		return fmt.Sprintf("%d–%d", start, end)
	}
	return strconv.Itoa(start)
}

// urlHost returns the host of a URL string, or "" when it can't be parsed to a
// clean host (so a malformed/absent URL falls back to the generic breadcrumb and
// never leaks a query string).
func urlHost(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}
