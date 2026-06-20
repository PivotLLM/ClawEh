package telegram

import (
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/utils"
)

// TestWrapMarkdownTables_EmojiAligns guards the mixed-emoji alignment bug:
// padding by rune count over-padded "🌙" rows (1 rune) relative to "☀️" rows
// (2 runes) though both render the same width. With display-width padding every
// rebuilt row ends at the same display width.
func TestWrapMarkdownTables_EmojiAligns(t *testing.T) {
	input := "| ☀️ Morning | ☀️ Sunny | 17°C | 10% |\n" +
		"| --- | --- | --- | --- |\n" +
		"| 🌙 Evening | 🌙 Clear | 19°C | 43% |\n" +
		"| 🌙 Night | 🌙 Clear | 15°C | 30% |"
	out := wrapMarkdownTables(input)

	var widths []int
	for _, line := range strings.Split(out, "\n") {
		if line == "```" || line == "" || strings.HasPrefix(line, "─") {
			continue
		}
		if strings.Contains(line, "|") {
			t.Fatalf("output should not contain pipe borders:\n%s", out)
		}
		widths = append(widths, utils.DisplayWidth(line))
	}
	if len(widths) < 2 {
		t.Fatalf("expected multiple table rows, got %d:\n%s", len(widths), out)
	}
	for i, w := range widths {
		if w != widths[0] {
			t.Fatalf("row %d display width %d != row 0 width %d (misaligned)\n%s",
				i, w, widths[0], out)
		}
	}
}

func TestMarkdownToTelegramHTML_HRuleSubstitute(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "bare dashes become divider",
			input: "Above\n---\nBelow",
			want:  "Above\n" + hRuleSubstitute + "\nBelow",
		},
		{
			name:  "bare asterisks become divider",
			input: "Above\n***\nBelow",
			want:  "Above\n" + hRuleSubstitute + "\nBelow",
		},
		{
			name:  "bare underscores become divider",
			input: "Above\n___\nBelow",
			want:  "Above\n" + hRuleSubstitute + "\nBelow",
		},
		{
			name:  "thematic break with surrounding spaces",
			input: "Above\n  ---  \nBelow",
			want:  "Above\n" + hRuleSubstitute + "\nBelow",
		},
		{
			name:  "longer run of dashes still substitutes",
			input: "Above\n------\nBelow",
			want:  "Above\n" + hRuleSubstitute + "\nBelow",
		},
		{
			name:  "two adjacent dash rules separated by blank line collapse",
			input: "Above\n\n---\n\n---\n\nBelow",
			want:  "Above\n\n" + hRuleSubstitute + "\n\nBelow",
		},
		{
			name:  "two adjacent dash rules on consecutive lines collapse",
			input: "Above\n---\n---\nBelow",
			want:  "Above\n" + hRuleSubstitute + "\nBelow",
		},
		{
			name:  "mixed thematic markers collapse to single divider",
			input: "Above\n\n***\n---\n___\n\nBelow",
			want:  "Above\n\n" + hRuleSubstitute + "\n\nBelow",
		},
		{
			name:  "single dash rule substitutes (no collapse)",
			input: "Above\n---\nBelow",
			want:  "Above\n" + hRuleSubstitute + "\nBelow",
		},
		{
			name:  "rules with text between are not collapsed",
			input: "---\nsome text\n---",
			want:  hRuleSubstitute + "\nsome text\n" + hRuleSubstitute,
		},
		{
			name:  "dash with trailing text is not a thematic break",
			input: "--- text",
			want:  "--- text",
		},
		{
			name:  "text followed by dashes is not a thematic break",
			input: "text ---",
			want:  "text ---",
		},
		{
			name:  "dashes joined to letters are not a thematic break",
			input: "---abc",
			want:  "---abc",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := markdownToTelegramHTML(tc.input)
			if got != tc.want {
				t.Errorf("markdownToTelegramHTML(%q)\n got:  %q\n want: %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestMarkdownToTelegramHTML_HRuleInsideFencedCode(t *testing.T) {
	// A thematic break inside a fenced code block must NOT be substituted.
	input := "before\n```\n---\n***\n___\n```\nafter"
	got := markdownToTelegramHTML(input)
	if strings.Contains(got, hRuleSubstitute) {
		t.Errorf("expected no divider substitution inside fenced code block, got: %q", got)
	}
	for _, marker := range []string{"---", "***", "___"} {
		if !strings.Contains(got, marker) {
			t.Errorf("expected marker %q preserved inside fenced code block, got: %q", marker, got)
		}
	}
}

func TestMarkdownToTelegramHTML_HRuleInsideInlineCode(t *testing.T) {
	// A thematic-break-shaped token inside inline code (single backticks) must
	// NOT be substituted. The inline code itself stays escaped inside <code>.
	input := "see `---` for the divider"
	got := markdownToTelegramHTML(input)
	if strings.Contains(got, hRuleSubstitute) {
		t.Errorf("expected no divider substitution inside inline code, got: %q", got)
	}
	if !strings.Contains(got, "<code>---</code>") {
		t.Errorf("expected inline code preserved as <code>---</code>, got: %q", got)
	}
}

func TestMarkdownToTelegramHTML_DisplayBlockFences(t *testing.T) {
	// A display:true payload arrives shaped like:
	//   ---
	//   header
	//
	//   payload body
	//   ---
	// Both fences should render as the long divider, and there should be
	// exactly two dividers (no extra collapse-induced duplication).
	input := "---\nHeader Line\n\npayload body\n---"
	got := markdownToTelegramHTML(input)

	count := strings.Count(got, hRuleSubstitute)
	if count != 2 {
		t.Errorf("expected exactly 2 dividers in display-block output, got %d: %q", count, got)
	}
	if strings.Contains(got, "\n---\n") || strings.HasPrefix(got, "---\n") || strings.HasSuffix(got, "\n---") {
		t.Errorf("expected both --- fences substituted, got: %q", got)
	}
	if !strings.Contains(got, "Header Line") || !strings.Contains(got, "payload body") {
		t.Errorf("expected payload content preserved, got: %q", got)
	}
}

func TestMarkdownToTelegramHTML_DisplayBlockFencesPayloadEndsWithRule(t *testing.T) {
	// If the payload itself ends with a thematic break, that rule + the
	// closing fence would stack. The collapse pass should reduce them to a
	// single divider.
	input := "---\nHeader\n\npayload\n---\n---"
	got := markdownToTelegramHTML(input)

	count := strings.Count(got, hRuleSubstitute)
	if count != 2 {
		t.Errorf("expected 2 dividers (opening + collapsed closing), got %d: %q", count, got)
	}
}

func TestMarkdownToTelegramHTML_MultilineHeadings(t *testing.T) {
	input := "## First Heading\nbody line\n## Second Heading\nmore body"
	got := markdownToTelegramHTML(input)
	// Both ## prefixes should be stripped — neither line should retain the
	// leading "## ".
	if strings.Contains(got, "## ") {
		t.Errorf("multiline headings: expected both ## prefixes stripped, got: %q", got)
	}
	if !strings.Contains(got, "First Heading") || !strings.Contains(got, "Second Heading") {
		t.Errorf("multiline headings: expected both heading texts present, got: %q", got)
	}
}

func TestMarkdownToTelegramHTML_MultilineBlockquote(t *testing.T) {
	input := "leading line\n> quoted line"
	got := markdownToTelegramHTML(input)
	// The "> " prefix on the second line should be stripped.
	if strings.Contains(got, "&gt; quoted line") || strings.Contains(got, "> quoted line") {
		t.Errorf("multiline blockquote: expected '> ' prefix stripped, got: %q", got)
	}
	if !strings.Contains(got, "quoted line") {
		t.Errorf("multiline blockquote: expected quoted text present, got: %q", got)
	}
}

func TestMarkdownToTelegramHTML_MultilineListItems(t *testing.T) {
	input := "- a\n- b\n- c"
	got := markdownToTelegramHTML(input)
	// Every "- " should have become a "• ".
	for _, want := range []string{"• a", "• b", "• c"} {
		if !strings.Contains(got, want) {
			t.Errorf("multiline list items: expected %q in output, got: %q", want, got)
		}
	}
	if strings.Contains(got, "- ") {
		t.Errorf("multiline list items: expected all '- ' prefixes converted, got: %q", got)
	}
}

func TestWrapMarkdownTables(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name: "plain dash separator",
			input: "| Name | Age |\n" +
				"| --- | --- |\n" +
				"| Alice | 30 |",
			want: "```\n" +
				"Name   Age\n" +
				"──────────\n" +
				"Alice  30\n" +
				"```",
		},
		{
			name: "alignment colons recognized as separator",
			input: "| Name | Age |\n" +
				"|:---|---:|\n" +
				"| Alice | 30 |",
			want: "```\n" +
				"Name   Age\n" +
				"──────────\n" +
				"Alice  30\n" +
				"```",
		},
		{
			name: "display width padding aligns non-ascii",
			input: "| Name | X |\n" +
				"| --- | --- |\n" +
				"| café | 1 |",
			want: "```\n" +
				"Name  X\n" +
				"───────\n" +
				"café  1\n" +
				"```",
		},
		{
			name: "table inside code fence is not touched",
			input: "```\n" +
				"| A | B |\n" +
				"| --- | --- |\n" +
				"```",
			want: "```\n" +
				"| A | B |\n" +
				"| --- | --- |\n" +
				"```",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := wrapMarkdownTables(tt.input); got != tt.want {
				t.Errorf("wrapMarkdownTables mismatch:\ninput: %q\ngot:   %q\nwant:  %q", tt.input, got, tt.want)
			}
		})
	}
}
