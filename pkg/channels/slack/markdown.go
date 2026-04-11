package slack

import (
	"strings"
	"unicode/utf8"
)

// formatSlackMessage converts markdown tables in the given text to monospace
// code blocks with a separator line. All other content is passed through unchanged.
//
// Markdown tables (pipe-delimited rows) are converted to the following format,
// wrapped in triple-backtick fences so Slack renders them in monospace:
//
//	Column A    Column B    Column C
//	────────────────────────────────
//	Row 1A      Row 1B      Row 1C
//	Row 2A      Row 2B      Row 2C
//
// The alignment row (|---|---|) is replaced by a Unicode box-drawing separator
// line (─, U+2500). Lines already inside a code fence are skipped.
func formatSlackMessage(text string) string {
	lines := strings.Split(text, "\n")
	var out []string
	inFence := false
	tableStart := -1

	// splitTableRow splits a pipe-delimited row into trimmed cell strings.
	splitTableRow := func(line string) []string {
		trimmed := strings.TrimSpace(line)
		trimmed = strings.TrimPrefix(trimmed, "|")
		trimmed = strings.TrimSuffix(trimmed, "|")
		parts := strings.Split(trimmed, "|")
		cells := make([]string, len(parts))
		for i, p := range parts {
			cells[i] = strings.TrimSpace(p)
		}
		return cells
	}

	// isSeparatorCell returns true when a cell contains only dashes.
	isSeparatorCell := func(cell string) bool {
		if len(cell) == 0 {
			return false
		}
		for _, ch := range cell {
			if ch != '-' {
				return false
			}
		}
		return true
	}

	// isSeparatorRow returns true when every cell in the row is a separator cell.
	isSeparatorRow := func(cells []string) bool {
		if len(cells) == 0 {
			return false
		}
		for _, c := range cells {
			if !isSeparatorCell(c) {
				return false
			}
		}
		return true
	}

	// renderTableBlock converts a slice of raw table lines into the target format:
	// a header row, a separator line of ─ characters, then data rows. Columns are
	// padded to align using rune width (not byte length).
	renderTableBlock := func(tableLines []string) []string {
		// Parse all rows into cells, skipping the markdown separator row.
		type row struct {
			cells []string
			isSep bool
		}
		parsed := make([]row, 0, len(tableLines))
		for _, l := range tableLines {
			cells := splitTableRow(l)
			parsed = append(parsed, row{cells: cells, isSep: isSeparatorRow(cells)})
		}

		// Determine max column count (excluding separator rows).
		maxCols := 0
		for _, r := range parsed {
			if !r.isSep && len(r.cells) > maxCols {
				maxCols = len(r.cells)
			}
		}
		if maxCols == 0 {
			return tableLines
		}

		// Find max rune-width per column across all non-separator rows.
		colWidth := make([]int, maxCols)
		for colIdx := range colWidth {
			colWidth[colIdx] = 1 // minimum width
		}
		for _, r := range parsed {
			if r.isSep {
				continue
			}
			for colIdx, cell := range r.cells {
				if colIdx >= maxCols {
					break
				}
				w := utf8.RuneCountInString(cell)
				if w > colWidth[colIdx] {
					colWidth[colIdx] = w
				}
			}
		}

		// Compute total line width for the separator: sum of column widths + spacing between columns.
		// Each column is padded to colWidth, and columns are separated by two spaces.
		totalWidth := 0
		for _, w := range colWidth {
			totalWidth += w
		}
		totalWidth += (maxCols - 1) * 2 // two spaces between columns

		var result []string
		separatorInserted := false

		for _, r := range parsed {
			if r.isSep {
				// Replace the markdown separator row with a Unicode line separator.
				result = append(result, strings.Repeat("─", totalWidth))
				separatorInserted = true
				continue
			}

			// Build the padded row.
			parts := make([]string, maxCols)
			for colIdx := range parts {
				var cell string
				if colIdx < len(r.cells) {
					cell = r.cells[colIdx]
				}
				// Pad with trailing spaces to colWidth using rune count.
				runeLen := utf8.RuneCountInString(cell)
				parts[colIdx] = cell + strings.Repeat(" ", colWidth[colIdx]-runeLen)
			}
			// Trim trailing spaces from the last column.
			parts[maxCols-1] = strings.TrimRight(parts[maxCols-1], " ")
			result = append(result, strings.Join(parts, "  "))
		}

		// If no markdown separator row was present, insert a separator after the first row.
		if !separatorInserted && len(result) > 0 {
			result = append(result[:1], append([]string{strings.Repeat("─", totalWidth)}, result[1:]...)...)
		}

		return result
	}

	flush := func(end int) {
		if tableStart < 0 {
			return
		}
		rendered := renderTableBlock(lines[tableStart:end])
		out = append(out, "```")
		out = append(out, rendered...)
		out = append(out, "```")
		tableStart = -1
	}

	isTableRow := func(line string) bool {
		trimmed := strings.TrimSpace(line)
		return strings.HasPrefix(trimmed, "|") && strings.HasSuffix(trimmed, "|")
	}

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			if inFence {
				inFence = false
				out = append(out, line)
				continue
			}
			// Entering a fence — flush any open table block first.
			flush(i)
			inFence = true
			out = append(out, line)
			continue
		}

		if inFence {
			out = append(out, line)
			continue
		}

		if isTableRow(line) {
			if tableStart < 0 {
				tableStart = i
			}
			// Defer appending; the block will be flushed when it ends.
			continue
		}

		// Not a table row — flush any open table block.
		flush(i)
		out = append(out, line)
	}

	// Flush any trailing table block.
	flush(len(lines))

	return strings.Join(out, "\n")
}
