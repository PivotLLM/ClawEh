package slack

import (
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/utils"
)

// TestFormatSlackMessage_EmojiTableAligns guards the mixed-emoji alignment bug:
// "☀️" (U+2600 U+FE0F, 2 runes) and "🌙" (U+1F319, 1 rune) render at the same
// width, so padding by rune count over-padded the moon rows and pushed later
// columns right. With display-width padding every data row ends at the same
// display width.
func TestFormatSlackMessage_EmojiTableAligns(t *testing.T) {
	input := "| ☀️ Morning | ☀️ Sunny | 17°C | 10% |\n" +
		"| --- | --- | --- | --- |\n" +
		"| ☀️ Noon | ☀️ Sunny | 19°C | 26% |\n" +
		"| 🌙 Evening | 🌙 Clear | 19°C | 43% |\n" +
		"| 🌙 Night | 🌙 Clear | 15°C | 30% |"
	out := formatSlackMessage(input)

	var widths []int
	for _, line := range strings.Split(out, "\n") {
		if line == "```" || strings.HasPrefix(line, "─") || line == "" {
			continue
		}
		widths = append(widths, utils.DisplayWidth(line))
	}
	for i, w := range widths {
		if w != widths[0] {
			t.Fatalf("row %d display width %d != row 0 width %d (columns misaligned)\n%s",
				i, w, widths[0], out)
		}
	}
}

func TestFormatSlackMessage(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "no table",
			input: "Hello world",
			want:  "Hello world",
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
		{
			name: "basic table with separator row",
			input: "| Column A | Column B | Column C |\n" +
				"| --- | --- | --- |\n" +
				"| Row 1A | Row 1B | Row 1C |\n" +
				"| Row 2A | Row 2B | Row 2C |",
			want: "```\n" +
				"Column A  Column B  Column C\n" +
				"────────────────────────────\n" +
				"Row 1A    Row 1B    Row 1C\n" +
				"Row 2A    Row 2B    Row 2C\n" +
				"```",
		},
		{
			name: "table without separator row",
			input: "| Col A | Col B |\n" +
				"| val1 | val2 |",
			want: "```\n" +
				"Col A  Col B\n" +
				"────────────\n" +
				"val1   val2\n" +
				"```",
		},
		{
			name: "table with text before and after",
			input: "Some text before.\n\n" +
				"| Name | Age |\n" +
				"| --- | --- |\n" +
				"| Alice | 30 |\n\n" +
				"Some text after.",
			want: "Some text before.\n\n" +
				"```\n" +
				"Name   Age\n" +
				"──────────\n" +
				"Alice  30\n" +
				"```\n\n" +
				"Some text after.",
		},
		{
			name: "table inside code fence is not converted",
			input: "```\n" +
				"| Col A | Col B |\n" +
				"| --- | --- |\n" +
				"| val1 | val2 |\n" +
				"```",
			want: "```\n" +
				"| Col A | Col B |\n" +
				"| --- | --- |\n" +
				"| val1 | val2 |\n" +
				"```",
		},
		{
			name: "table before code fence",
			input: "| A | B |\n" +
				"| - | - |\n" +
				"| 1 | 2 |\n" +
				"```\nsome code\n```",
			want: "```\n" +
				"A  B\n" +
				"────\n" +
				"1  2\n" +
				"```\n" +
				"```\nsome code\n```",
		},
		{
			name: "columns padded to widest content",
			input: "| Short | A Very Long Header |\n" +
				"| --- | --- |\n" +
				"| x | y |",
			want: "```\n" +
				"Short  A Very Long Header\n" +
				"─────────────────────────\n" +
				"x      y\n" +
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
			name: "center alignment colons recognized as separator",
			input: "| A | B |\n" +
				"| :-: | :-: |\n" +
				"| 1 | 2 |",
			want: "```\n" +
				"A  B\n" +
				"────\n" +
				"1  2\n" +
				"```",
		},
		{
			name: "multiple separate tables",
			input: "| A | B |\n" +
				"| - | - |\n" +
				"| 1 | 2 |\n\n" +
				"| C | D |\n" +
				"| - | - |\n" +
				"| 3 | 4 |",
			want: "```\n" +
				"A  B\n" +
				"────\n" +
				"1  2\n" +
				"```\n\n" +
				"```\n" +
				"C  D\n" +
				"────\n" +
				"3  4\n" +
				"```",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatSlackMessage(tt.input)
			if got != tt.want {
				t.Errorf("formatSlackMessage mismatch:\ninput: %q\ngot:   %q\nwant:  %q",
					tt.input, got, tt.want)
				// Also show a diff-friendly view
				gotLines := strings.Split(got, "\n")
				wantLines := strings.Split(tt.want, "\n")
				t.Logf("got lines (%d):", len(gotLines))
				for i, l := range gotLines {
					t.Logf("  [%d] %q", i, l)
				}
				t.Logf("want lines (%d):", len(wantLines))
				for i, l := range wantLines {
					t.Logf("  [%d] %q", i, l)
				}
			}
		})
	}
}
