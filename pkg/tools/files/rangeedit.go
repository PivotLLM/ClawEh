package files

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/PivotLLM/ClawEh/pkg/tools"
)

// rangeEditTool implements the six explicit positional mutation tools so the LLM
// edits by the same addressing it read/searched with and never has to restate
// existing content (the fragile part of anchor file_edit). Three verbs × two units:
//
//	file_edit_lines / file_edit_bytes     replace a range (refuses empty replace)
//	file_insert_lines / file_insert_bytes insert at a position (removes nothing)
//	file_delete_lines / file_delete_bytes delete a range
//
// Deletion is its own verb so an empty/missing replace can never silently wipe a
// span. backup defaults on for the destructive verbs (edit, delete) and off for
// insert. end is optional and defaults to end-of-file.
type rangeEditTool struct {
	sysFs fileSystem
	op    string // "edit" | "insert" | "delete"
	unit  string // "lines" | "bytes"
}

func newRangeEditTool(op, unit, workspace string, restrict bool, writeSubdir string, allowPaths ...[]*regexp.Regexp) *rangeEditTool {
	var patterns []*regexp.Regexp
	if len(allowPaths) > 0 {
		patterns = allowPaths[0]
	}
	return &rangeEditTool{sysFs: buildWriteFs(workspace, restrict, writeSubdir, patterns), op: op, unit: unit}
}

func (t *rangeEditTool) Name() string { return "file_" + t.op + "_" + t.unit }

// backupDefault: on for destructive verbs, off for insert (additive).
func (t *rangeEditTool) backupDefault() bool { return t.op != "insert" }

func (t *rangeEditTool) Description() string {
	switch t.op + "_" + t.unit {
	case "edit_lines":
		return "Replace lines start..end (1-based; end optional, defaults to end of file) with `replace`. To remove lines use file_delete_lines."
	case "edit_bytes":
		return "Replace bytes start..end (0-based, inclusive; end optional, defaults to end of file) with `replace`. To remove bytes use file_delete_bytes."
	case "insert_lines":
		return "Insert `text` after a line number (0 = top of file). Nothing is removed."
	case "insert_bytes":
		return "Insert `text` at a byte offset (0 = top of file). Nothing is removed."
	case "delete_lines":
		return "Delete lines start..end (1-based; end optional, defaults to end of file)."
	case "delete_bytes":
		return "Delete bytes start..end (0-based, inclusive; end optional, defaults to end of file)."
	}
	return ""
}

func (t *rangeEditTool) Parameters() map[string]any {
	props := map[string]any{
		"path":    map[string]any{"type": "string", "description": "File to modify."},
		"backup":  map[string]any{"type": "boolean", "description": "Copy the file to <file>.NNNN before modifying.", "default": t.backupDefault()},
		"display": map[string]any{"type": "boolean", "description": "Also show the affected content to the user.", "default": false},
	}
	var required []string
	switch t.op {
	case "insert":
		if t.unit == "lines" {
			props["after_line"] = map[string]any{"type": "integer", "description": "Insert after this 1-based line (0 = top of file)."}
			required = []string{"path", "after_line", "text"}
		} else {
			props["at_offset"] = map[string]any{"type": "integer", "description": "Insert at this 0-based byte offset (0 = top of file)."}
			required = []string{"path", "at_offset", "text"}
		}
		props["text"] = map[string]any{"type": "string", "description": "Content to insert (non-empty)."}
	case "delete":
		props["start"] = t.startSchema()
		props["end"] = t.endSchema()
		required = []string{"path", "start"}
	default: // edit
		props["start"] = t.startSchema()
		props["end"] = t.endSchema()
		props["replace"] = map[string]any{"type": "string", "description": "Replacement content (non-empty; use file_delete_" + t.unit + " to remove)."}
		required = []string{"path", "start", "replace"}
	}
	return map[string]any{"type": "object", "properties": props, "required": required}
}

func (t *rangeEditTool) startSchema() map[string]any {
	if t.unit == "lines" {
		return map[string]any{"type": "integer", "description": "First line (1-based; 0 = start of file)."}
	}
	return map[string]any{"type": "integer", "description": "First byte (0-based; 0 = start of file)."}
}

func (t *rangeEditTool) endSchema() map[string]any {
	if t.unit == "lines" {
		return map[string]any{"type": "integer", "description": "Last line (1-based, inclusive). Omit or 0 for end of file."}
	}
	return map[string]any{"type": "integer", "description": "Last byte (0-based, inclusive). Omit for end of file."}
}

func (t *rangeEditTool) Execute(_ context.Context, args map[string]any) *tools.ToolResult {
	path, ok := args["path"].(string)
	if !ok {
		return tools.ErrorResult("path is required")
	}
	content, err := t.sysFs.ReadFile(path)
	if err != nil {
		return tools.ErrorResult(err.Error())
	}

	var newContent []byte
	var report, shown string
	if t.unit == "lines" {
		newContent, report, shown, err = t.applyLines(content, args)
	} else {
		newContent, report, shown, err = t.applyBytes(content, args)
	}
	if err != nil {
		return tools.ErrorResult(err.Error())
	}

	if getBoolArg(args, "backup", t.backupDefault()) {
		if _, berr := backupExistingFile(t.sysFs, path); berr != nil {
			return tools.ErrorResult(berr.Error())
		}
	}
	if werr := t.sysFs.WriteFile(path, newContent); werr != nil {
		return tools.ErrorResult(werr.Error())
	}

	forLLM := fmt.Sprintf("%s in %s", report, path)
	if getBoolArg(args, "display", false) && shown != "" {
		return &tools.ToolResult{ForLLM: forLLM, ForUser: displayBody(displayHeader(report, path), shown)}
	}
	return tools.SilentResult(forLLM)
}

func (t *rangeEditTool) applyLines(content []byte, args map[string]any) (out []byte, report, shown string, err error) {
	lines, trailingNL := splitLines(content)
	n := int64(len(lines))

	if t.op == "insert" {
		text, ok := args["text"].(string)
		if !ok || text == "" {
			return nil, "", "", fmt.Errorf("text is required (non-empty)")
		}
		after, _ := getInt64Arg(args, "after_line", 0)
		if after < 0 || after > n {
			return nil, "", "", fmt.Errorf("after_line %d out of range (file has %d line(s); use 0..%d)", after, n, n)
		}
		ins, _ := splitLines([]byte(text))
		merged := append(append(append([]string{}, lines[:after]...), ins...), lines[after:]...)
		return joinLines(merged, trailingNL), fmt.Sprintf("Inserted %d line(s) after line %d", len(ins), after), text, nil
	}

	if n == 0 {
		return nil, "", "", fmt.Errorf("file has no lines")
	}
	start, _ := getInt64Arg(args, "start", 1)
	if start <= 0 {
		start = 1
	}
	end, _ := getInt64Arg(args, "end", 0)
	if end <= 0 || end > n {
		end = n
	}
	if start > n {
		return nil, "", "", fmt.Errorf("start %d is past end of file (file has %d line(s); use file_insert_lines to add)", start, n)
	}
	if end < start {
		return nil, "", "", fmt.Errorf("end %d is before start %d", end, start)
	}
	removed := strings.Join(lines[start-1:end], "\n")
	nRemoved := end - start + 1

	if t.op == "delete" {
		merged := append(append([]string{}, lines[:start-1]...), lines[end:]...)
		return joinLines(merged, trailingNL), fmt.Sprintf("Deleted lines %d-%d (%d line(s))", start, end, nRemoved), removed, nil
	}
	// edit
	replace, _ := args["replace"].(string)
	if replace == "" {
		return nil, "", "", fmt.Errorf("replace must be non-empty; to remove lines use file_delete_lines")
	}
	ins, _ := splitLines([]byte(replace))
	merged := append(append(append([]string{}, lines[:start-1]...), ins...), lines[end:]...)
	return joinLines(merged, trailingNL), fmt.Sprintf("Replaced lines %d-%d (%d→%d line(s))", start, end, nRemoved, len(ins)), replace, nil
}

func (t *rangeEditTool) applyBytes(content []byte, args map[string]any) (out []byte, report, shown string, err error) {
	n := int64(len(content))

	if t.op == "insert" {
		text, ok := args["text"].(string)
		if !ok || text == "" {
			return nil, "", "", fmt.Errorf("text is required (non-empty)")
		}
		at, _ := getInt64Arg(args, "at_offset", 0)
		if at < 0 || at > n {
			return nil, "", "", fmt.Errorf("at_offset %d out of range (file is %d bytes; use 0..%d)", at, n, n)
		}
		merged := append(append(append([]byte{}, content[:at]...), []byte(text)...), content[at:]...)
		return merged, fmt.Sprintf("Inserted %d byte(s) at offset %d", len(text), at), text, nil
	}

	if n == 0 {
		return nil, "", "", fmt.Errorf("file is empty")
	}
	start, _ := getInt64Arg(args, "start", 0)
	if start < 0 {
		return nil, "", "", fmt.Errorf("start must be >= 0")
	}
	if start >= n {
		return nil, "", "", fmt.Errorf("start %d is at/past end of file (file is %d bytes; use file_insert_bytes to add)", start, n)
	}
	end := n - 1
	if argPresent(args, "end") {
		end, _ = getInt64Arg(args, "end", n-1)
		if end > n-1 {
			end = n - 1
		}
	}
	if end < start {
		return nil, "", "", fmt.Errorf("end %d is before start %d", end, start)
	}
	removed := string(content[start : end+1])
	nRemoved := end - start + 1

	if t.op == "delete" {
		merged := append(append([]byte{}, content[:start]...), content[end+1:]...)
		return merged, fmt.Sprintf("Deleted bytes %d-%d (%d byte(s))", start, end, nRemoved), removed, nil
	}
	// edit
	replace, _ := args["replace"].(string)
	if replace == "" {
		return nil, "", "", fmt.Errorf("replace must be non-empty; to remove bytes use file_delete_bytes")
	}
	merged := append(append(append([]byte{}, content[:start]...), []byte(replace)...), content[end+1:]...)
	return merged, fmt.Sprintf("Replaced bytes %d-%d (%d→%d byte(s))", start, end, nRemoved, len(replace)), replace, nil
}

// splitLines splits content into lines (without the terminating newline) and
// reports whether the file ended with a newline, so joinLines can restore it.
func splitLines(content []byte) ([]string, bool) {
	if len(content) == 0 {
		return nil, false
	}
	s := string(content)
	trailing := strings.HasSuffix(s, "\n")
	if trailing {
		s = s[:len(s)-1]
	}
	return strings.Split(s, "\n"), trailing
}

func joinLines(lines []string, trailingNL bool) []byte {
	if len(lines) == 0 {
		return []byte{}
	}
	s := strings.Join(lines, "\n")
	if trailingNL {
		s += "\n"
	}
	return []byte(s)
}
