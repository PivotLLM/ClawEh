package telegram

import (
	"strings"
	"testing"
)

func TestMarkdownToTelegramHTML_HRuleCollapse(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "two adjacent dash rules separated by blank line collapse",
			input: "Above\n\n---\n\n---\n\nBelow",
			want:  "Above\n\n---\n\nBelow",
		},
		{
			name:  "two adjacent dash rules on consecutive lines collapse",
			input: "Above\n---\n---\nBelow",
			want:  "Above\n---\nBelow",
		},
		{
			name:  "mixed thematic markers collapse to single rule",
			input: "Above\n\n***\n---\n___\n\nBelow",
			want:  "Above\n\n---\n\nBelow",
		},
		{
			name:  "single dash rule is unchanged",
			input: "Above\n---\nBelow",
			want:  "Above\n---\nBelow",
		},
		{
			name:  "rules with text between are not collapsed",
			input: "---\nsome text\n---",
			want:  "---\nsome text\n---",
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
