package agent

import (
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
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

	switch name {
	case "file_read_lines":
		if base := toolArgBase(args, "path"); base != "" {
			return prefix + "file_read " + readLineRange(args) + " " + base
		}
	case "file_read_bytes":
		if base := toolArgBase(args, "path"); base != "" {
			return prefix + "file_read " + base
		}
	case "file_edit_lines", "file_delete_lines", "file_insert_lines":
		if base := toolArgBase(args, "path"); base != "" {
			verb := map[string]string{
				"file_edit_lines":   "file_edit",
				"file_delete_lines": "file_delete",
				"file_insert_lines": "file_insert",
			}[name]
			r := editLineRange(args)
			if r != "" {
				r = " " + r
			}
			return prefix + verb + r + " " + base
		}
	case "file_edit", "file_write", "file_append", "file_delete":
		if base := toolArgBase(args, "path"); base != "" {
			return prefix + name + " " + base
		}
	case "file_list":
		base := toolArgBase(args, "path")
		if base == "" {
			base = "."
		}
		return prefix + "file_list " + base
	case "file_search_lines", "file_search_bytes":
		if base := toolArgBase(args, "path"); base != "" {
			return prefix + "file_search " + base
		}
	case "file_move", "file_copy":
		src := toolArgBase(args, "source_path")
		dst := toolArgBase(args, "destination_path")
		if src != "" || dst != "" {
			return prefix + name + " " + src + " → " + dst
		}
	case "web_fetch":
		if h := urlHost(toolArgStr(args, "url")); h != "" {
			return prefix + "web_fetch " + h
		}
	}
	// Generic fallback: just the (namespace-stripped) tool name — no args, so no
	// chance of leaking content from an unknown tool's arguments.
	return prefix + name
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

// readLineRange renders a file_read_lines slice as "start–end" (from start_line +
// line_count) or just "start" when no count is given.
func readLineRange(args map[string]any) string {
	start, ok := toolArgInt(args, "start_line")
	if !ok || start <= 0 {
		start = 1
	}
	if count, ok := toolArgInt(args, "line_count"); ok && count > 0 {
		return fmt.Sprintf("%d–%d", start, start+count-1)
	}
	return strconv.Itoa(start)
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
