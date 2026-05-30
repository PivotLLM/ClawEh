package tools

import (
	"encoding/json"
	"fmt"
	"strings"
)

// RedactArgs is the exported entry point for log-redacting tool arguments
// from callers outside this package (pkg/agent loop, pkg/tools toolloop).
// It delegates to the package-internal redactArgs implementation so that
// the registry.go INF redaction (locked in by QA d18c6da6) and external
// log sites share a single source of truth.
//
// The fallback path for unknown tools bounds output via JSON marshal +
// rune-counted truncation. It deliberately does not consult utils.Truncate
// so the global --no-truncate flag cannot uncap INF redaction.
func RedactArgs(toolName string, args map[string]any) any {
	return redactArgs(toolName, args)
}

// redactArgs returns a log-safe summary of a tool's arguments. Sensitive
// payloads (file contents, edit text, HTTP bodies) are replaced with byte
// counts so INF-level logs never persist user-supplied content.
func redactArgs(toolName string, args map[string]any) any {
	if args == nil {
		return map[string]any{}
	}

	switch toolName {
	case "write_file", "files_write":
		out := map[string]any{
			"path":          args["path"],
			"content_bytes": byteLen(args["content"]),
		}
		if v, ok := args["overwrite"]; ok && v != nil {
			out["overwrite"] = v
		}
		return out
	case "append_file", "files_append":
		return map[string]any{
			"path":          args["path"],
			"content_bytes": byteLen(args["content"]),
		}
	case "read_file", "files_read":
		out := map[string]any{"path": args["path"]}
		if v, ok := args["offset"]; ok && v != nil {
			out["offset"] = v
		}
		if v, ok := args["length"]; ok && v != nil {
			out["length"] = v
		}
		return out
	case "edit_file", "files_edit":
		return map[string]any{
			"path":           args["path"],
			"old_text_bytes": byteLen(args["old_text"]),
			"new_text_bytes": byteLen(args["new_text"]),
		}
	}

	if strings.HasPrefix(toolName, "http_") || toolName == "web_fetch" {
		out := map[string]any{}
		if v, ok := args["url"]; ok && v != nil {
			out["url"] = v
		}
		if v, ok := args["method"]; ok && v != nil {
			out["method"] = v
		}
		return out
	}

	return truncateForRedaction(args, 200)
}

// byteLen returns the byte length of a string-shaped value, or 0 otherwise.
func byteLen(v any) int {
	if s, ok := v.(string); ok {
		return len(s)
	}
	return 0
}

// truncateForRedaction JSON-encodes args and caps the result at maxLen runes
// with a "...(N more)" suffix. It deliberately does not use utils.Truncate
// because the global --no-truncate flag must not bypass INF-level redaction.
func truncateForRedaction(args map[string]any, maxLen int) string {
	b, err := json.Marshal(args)
	if err != nil {
		return fmt.Sprintf("<unmarshalable args: %v>", err)
	}
	s := string(b)
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	if maxLen <= 0 {
		return ""
	}
	excess := len(runes) - maxLen
	return string(runes[:maxLen]) + fmt.Sprintf("...(%d more)", excess)
}
