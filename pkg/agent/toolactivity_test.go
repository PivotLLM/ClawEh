package agent

import (
	"strings"
	"testing"
)

func TestToolActivitySummary(t *testing.T) {
	cases := []struct {
		desc    string
		tool    string
		args    map[string]any
		want    string // exact expected breadcrumb
		notWant string // must NOT appear (privacy)
	}{
		{
			desc: "read line range",
			tool: "file_read_lines",
			args: map[string]any{"path": "files/novels/natalie1/outline.md", "start_line": 1.0, "line_count": 250.0},
			want: "🔧 file_read lines 1–250 `outline.md`",
		},
		{
			desc: "read no count shows full default range",
			tool: "file_read_lines",
			args: map[string]any{"path": "outline.md", "start_line": 716.0},
			want: "🔧 file_read lines 716–965 `outline.md`",
		},
		{
			desc: "read bytes labelled",
			tool: "file_read_bytes",
			args: map[string]any{"path": "outline.md", "offset": 4096.0},
			want: "🔧 file_read bytes `outline.md`",
		},
		{
			desc:    "edit lines hides replacement text",
			tool:    "file_edit_lines",
			args:    map[string]any{"path": "outline.md", "start": 10.0, "end": 12.0, "replace": "SECRET NEW PROSE"},
			want:    "🔧 file_edit 10–12 `outline.md`",
			notWant: "SECRET",
		},
		{
			desc:    "file_edit hides old/new text",
			tool:    "file_edit",
			args:    map[string]any{"path": "a.md", "old_text": "secret old", "new_text": "secret new"},
			want:    "🔧 file_edit `a.md`",
			notWant: "secret",
		},
		{
			desc:    "file_write hides content",
			tool:    "file_write",
			args:    map[string]any{"path": "b.md", "content": "TOP SECRET BODY"},
			want:    "🔧 file_write `b.md`",
			notWant: "SECRET",
		},
		{
			desc:    "web_fetch shows host only",
			tool:    "web_fetch",
			args:    map[string]any{"url": "https://example.com/path?q=secretquery"},
			want:    "🔧 web_fetch `example.com`",
			notWant: "secretquery",
		},
		{
			desc: "file_list default path",
			tool: "file_list",
			args: map[string]any{},
			want: "🔧 file_list `.`",
		},
		{
			desc: "mcp namespace stripped",
			tool: "mcp__www__browser_click",
			args: nil,
			want: "🔧 `browser_click`",
		},
		{
			desc:    "underscored tool name is backticked (not italicised by telegram)",
			tool:    "mcp__claw__cogmem_memory_create",
			args:    nil,
			want:    "🔧 `cogmem_memory_create`",
		},
		{
			desc:    "unknown tool shows name only, no args",
			tool:    "some_unknown_tool",
			args:    map[string]any{"secret_arg": "leak me"},
			want:    "🔧 `some_unknown_tool`",
			notWant: "leak me",
		},
	}

	for _, c := range cases {
		got := toolActivitySummary(c.tool, c.args)
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.desc, got, c.want)
		}
		if c.notWant != "" && strings.Contains(got, c.notWant) {
			t.Errorf("%s: breadcrumb leaked %q: %q", c.desc, c.notWant, got)
		}
	}
}

func TestToolActivitySummary_EmptyName(t *testing.T) {
	if got := toolActivitySummary("", nil); got != "" {
		t.Fatalf("empty name should yield empty summary, got %q", got)
	}
}
