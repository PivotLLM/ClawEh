// ClawEh
// License: MIT

package audit

import (
	"slices"
	"strings"
	"unicode/utf8"
)

// RenderText renders the report as plain text: title, generation time, each
// section with its notes, tables as aligned columns, and subsections indented
// under their section. Highlighted rows are marked with "!".
func RenderText(r *Report) string {
	var b strings.Builder
	b.WriteString(r.Product)
	if r.TagLine != "" {
		b.WriteString(" - " + r.TagLine)
	}
	b.WriteString("\nVersion " + r.Version + "\nConfiguration as of " + r.GeneratedAt.Format(timeFormat) + "\n")
	for _, s := range r.Sections {
		b.WriteString("\n== " + s.Title + " ==\n")
		writeSectionText(&b, s, "")
		for _, sub := range s.Subsections {
			b.WriteString("\n  -- " + sub.Title + " --\n")
			writeSectionText(&b, sub, "  ")
		}
	}
	return b.String()
}

func writeSectionText(b *strings.Builder, s Section, indent string) {
	for _, n := range s.Notes {
		b.WriteString(indent + n + "\n")
	}
	for _, t := range s.Tables {
		b.WriteString("\n")
		if t.Caption != "" {
			b.WriteString(indent + t.Caption + ":\n")
		}
		writeTableText(b, t, indent)
	}
}

func writeTableText(b *strings.Builder, t Table, indent string) {
	widths := make([]int, len(t.Columns))
	for i, c := range t.Columns {
		widths[i] = utf8.RuneCountInString(c)
	}
	for _, r := range t.Rows {
		for i, cell := range r {
			if i < len(widths) {
				widths[i] = max(widths[i], utf8.RuneCountInString(cell))
			}
		}
	}
	line := func(mark string, cells []string) {
		b.WriteString(indent + mark + " ")
		for i := range t.Columns {
			cell := ""
			if i < len(cells) {
				cell = cells[i]
			}
			if i == len(t.Columns)-1 {
				b.WriteString(cell)
				break
			}
			b.WriteString(cell + strings.Repeat(" ", widths[i]-utf8.RuneCountInString(cell)) + "  ")
		}
		b.WriteString("\n")
	}
	line(" ", t.Columns)
	sep := make([]string, len(t.Columns))
	for i, w := range widths {
		sep[i] = strings.Repeat("-", w)
	}
	line(" ", sep)
	for i, r := range t.Rows {
		mark := " "
		if slices.Contains(t.Highlight, i) {
			mark = "!"
		}
		line(mark, r)
	}
}
