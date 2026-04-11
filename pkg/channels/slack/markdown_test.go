package slack

import (
	"strings"
	"testing"
)

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
