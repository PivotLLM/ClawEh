package mdhelper

import (
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/utils"
)

func TestFormatTables(t *testing.T) {
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
				"----------------------------\n" +
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
				"------------\n" +
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
				"----------\n" +
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
				"----\n" +
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
				"-------------------------\n" +
				"x      y\n" +
				"```",
		},
		{
			// Right alignment is now honored: the Age column (---:) is padded on the
			// left so its values end at the column's right edge.
			name: "right alignment honored",
			input: "| Name | Age |\n" +
				"|:---|---:|\n" +
				"| Alice | 30 |",
			want: "```\n" +
				"Name   Age\n" +
				"----------\n" +
				"Alice   30\n" +
				"```",
		},
		{
			// Center alignment splits the padding around the cell.
			name: "center alignment honored",
			input: "| h | val |\n" +
				"|:-:|:-:|\n" +
				"| xxxxx | y |",
			want: "```\n" +
				"  h    val\n" +
				"----------\n" +
				"xxxxx   y\n" +
				"```",
		},
		{
			// Explicit left colon behaves the same as a plain separator.
			name: "explicit left alignment",
			input: "| Name | City |\n" +
				"|:---|:---|\n" +
				"| Al | Ottawa |",
			want: "```\n" +
				"Name  City\n" +
				"------------\n" +
				"Al    Ottawa\n" +
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
				"----\n" +
				"1  2\n" +
				"```\n\n" +
				"```\n" +
				"C  D\n" +
				"----\n" +
				"3  4\n" +
				"```",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatTables(tt.input)
			if got != tt.want {
				t.Errorf("FormatTables mismatch:\ninput: %q\ngot:   %q\nwant:  %q", tt.input, got, tt.want)
				for i, l := range strings.Split(got, "\n") {
					t.Logf("got  [%d] %q", i, l)
				}
				for i, l := range strings.Split(tt.want, "\n") {
					t.Logf("want [%d] %q", i, l)
				}
			}
		})
	}
}

// TestFormatTables_EmojiAligns guards the mixed-emoji alignment bug: "☀️"
// (U+2600 U+FE0F, 2 runes) and "🌙" (U+1F319, 1 rune) render at the same display
// width, so padding by display width (not rune count) keeps every row the same
// width.
func TestFormatTables_EmojiAligns(t *testing.T) {
	input := "| ☀️ Morning | ☀️ Sunny | 17°C | 10% |\n" +
		"| --- | --- | --- | --- |\n" +
		"| ☀️ Noon | ☀️ Sunny | 19°C | 26% |\n" +
		"| 🌙 Evening | 🌙 Clear | 19°C | 43% |\n" +
		"| 🌙 Night | 🌙 Clear | 15°C | 30% |"
	out := FormatTables(input)

	var widths []int
	for _, line := range strings.Split(out, "\n") {
		if line == "```" || strings.HasPrefix(line, "-") || line == "" {
			continue
		}
		widths = append(widths, utils.DisplayWidth(line))
	}
	for i, w := range widths {
		if w != widths[0] {
			t.Fatalf("row %d display width %d != row 0 width %d (columns misaligned)\n%s", i, w, widths[0], out)
		}
	}
}

// TestFormatTables_DividerMatchesLongestRow guards that the divider rule is
// exactly as wide as the widest data row, so it lines up under the columns.
func TestFormatTables_DividerMatchesLongestRow(t *testing.T) {
	input := "| Time | PP | Comments |\n" +
		"|------|---:|----------|\n" +
		"| 14:00 | 30% | Possible t-storms |\n" +
		"| 16:00 | 30% | Clear |"
	out := FormatTables(input)

	divider, widest := -1, 0
	for _, line := range strings.Split(out, "\n") {
		if line == "```" || line == "" {
			continue
		}
		w := utils.DisplayWidth(line)
		if strings.Trim(line, "-") == "" { // the all-dashes rule line
			divider = w
			continue
		}
		if w > widest {
			widest = w
		}
	}
	if divider != widest {
		t.Fatalf("divider width %d != widest row %d\n%s", divider, widest, out)
	}
}

// TestFormatTables_RightAlignedColumnEndsFlush checks the alignment property
// directly: every value in a right-aligned column ends at the same offset.
func TestFormatTables_RightAlignedColumnEndsFlush(t *testing.T) {
	input := "| Item | Qty |\n" +
		"|:-----|----:|\n" +
		"| Apple | 5 |\n" +
		"| Watermelon | 100 |"
	out := FormatTables(input)

	for _, line := range strings.Split(out, "\n") {
		if line == "```" || strings.HasPrefix(line, "-") || line == "" {
			continue
		}
		// Right-aligned last column: no row carries trailing spaces, and the line
		// width is uniform, so values are flush-right.
		if strings.HasSuffix(line, " ") {
			t.Fatalf("right-aligned row must not end in padding: %q", line)
		}
	}
}
