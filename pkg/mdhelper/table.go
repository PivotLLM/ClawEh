// Package mdhelper holds small, dependency-light helpers for converting Markdown
// into forms suitable for chat channels. Each helper lives in its own file named
// for what it does (e.g. table.go for Markdown tables), so the package can grow a
// home for related conversions without entangling the channel providers.
package mdhelper

import (
	"strings"

	"github.com/PivotLLM/ClawEh/pkg/utils"
)

// align is a column's horizontal alignment, parsed from a Markdown table's
// separator row: ":---" left, "---:" right, ":---:" center; a plain "---"
// defaults to left.
type align int

const (
	alignLeft align = iota
	alignRight
	alignCenter
)

// FormatTables converts pipe-delimited Markdown tables in text into fixed-width
// blocks wrapped in triple-backtick fences, so chat clients that render fenced
// code in a monospace font (Slack, Telegram, …) display them as aligned tables.
// All other content — including anything already inside a code fence — passes
// through unchanged.
//
// Per-column alignment from the separator row is honored — left, right, and
// center. A table without a separator row gets a rule inserted after its first
// row, with every column defaulting to left.
//
// Example (the fences are added by this function):
//
//	| Item  | Qty |        Item    Qty
//	|:------|----:|   →    ───────────
//	| Apple |   5 |        Apple     5
func FormatTables(text string) string {
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	inFence := false
	tableStart := -1

	flush := func(end int) {
		if tableStart < 0 {
			return
		}
		out = append(out, "```")
		out = append(out, renderTableBlock(lines[tableStart:end])...)
		out = append(out, "```")
		tableStart = -1
	}

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			if inFence {
				inFence = false
				out = append(out, line)
				continue
			}
			flush(i) // entering a fence — close any open table block first
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
			continue // defer; the block is flushed when it ends
		}
		flush(i)
		out = append(out, line)
	}
	flush(len(lines))

	return strings.Join(out, "\n")
}

// isTableRow reports whether a line is a pipe-bordered table row.
func isTableRow(line string) bool {
	t := strings.TrimSpace(line)
	return strings.HasPrefix(t, "|") && strings.HasSuffix(t, "|")
}

// splitTableRow splits a pipe-delimited row into trimmed cell strings. The row is
// expected to start and end with '|'.
func splitTableRow(line string) []string {
	t := strings.TrimSpace(line)
	t = strings.TrimPrefix(t, "|")
	t = strings.TrimSuffix(t, "|")
	parts := strings.Split(t, "|")
	cells := make([]string, len(parts))
	for i, p := range parts {
		cells[i] = strings.TrimSpace(p)
	}
	return cells
}

// isSeparatorCell reports whether a cell is a Markdown table separator: dashes
// optionally bracketed by alignment colons (---, :---, ---:, :---:).
func isSeparatorCell(cell string) bool {
	if cell == "" {
		return false
	}
	hasDash := false
	for _, ch := range cell {
		switch ch {
		case '-':
			hasDash = true
		case ':':
			// alignment marker — allowed
		default:
			return false
		}
	}
	return hasDash
}

// isSeparatorRow reports whether every cell in a row is a separator cell.
func isSeparatorRow(cells []string) bool {
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

// cellAlign reads one separator cell's alignment from its colon markers.
func cellAlign(cell string) align {
	left := strings.HasPrefix(cell, ":")
	right := strings.HasSuffix(cell, ":")
	switch {
	case left && right:
		return alignCenter
	case right:
		return alignRight
	default:
		return alignLeft
	}
}

// padCell aligns cell within display width w according to a.
func padCell(cell string, w int, a align) string {
	d := utils.DisplayWidth(cell)
	if d >= w {
		return cell
	}
	gap := w - d
	switch a {
	case alignRight:
		return strings.Repeat(" ", gap) + cell
	case alignCenter:
		l := gap / 2
		return strings.Repeat(" ", l) + cell + strings.Repeat(" ", gap-l)
	default: // alignLeft
		return cell + strings.Repeat(" ", gap)
	}
}

// renderTableBlock turns raw Markdown table lines into aligned, border-free rows:
// a header, a ─ rule (replacing the separator row, or inserted after the first
// row when none is present), then data rows. Columns are padded to display width
// (so emoji/CJK align), separated by two spaces; per-column alignment is taken
// from the separator row.
func renderTableBlock(tableLines []string) []string {
	parsed := make([][]string, len(tableLines))
	isSep := make([]bool, len(tableLines))
	firstSep := -1
	for i, l := range tableLines {
		parsed[i] = splitTableRow(l)
		if isSeparatorRow(parsed[i]) {
			isSep[i] = true
			if firstSep < 0 {
				firstSep = i
			}
		}
	}

	// Column count from non-separator rows.
	maxCols := 0
	for i, cells := range parsed {
		if isSep[i] {
			continue
		}
		if len(cells) > maxCols {
			maxCols = len(cells)
		}
	}
	if maxCols == 0 {
		return tableLines
	}

	// Per-column alignment (default left), from the first separator row.
	aligns := make([]align, maxCols)
	if firstSep >= 0 {
		for c, cell := range parsed[firstSep] {
			if c >= maxCols {
				break
			}
			aligns[c] = cellAlign(cell)
		}
	}

	// Per-column display width (min 1), over non-separator rows.
	colWidth := make([]int, maxCols)
	for c := range colWidth {
		colWidth[c] = 1
	}
	for i, cells := range parsed {
		if isSep[i] {
			continue
		}
		for c, cell := range cells {
			if c >= maxCols {
				break
			}
			if w := utils.DisplayWidth(cell); w > colWidth[c] {
				colWidth[c] = w
			}
		}
	}

	// Divider length: padded columns plus the two-space gaps.
	totalWidth := (maxCols - 1) * 2
	for _, w := range colWidth {
		totalWidth += w
	}

	result := make([]string, 0, len(parsed))
	sepInserted := false
	for i, cells := range parsed {
		if isSep[i] {
			result = append(result, strings.Repeat("─", totalWidth))
			sepInserted = true
			continue
		}
		parts := make([]string, maxCols)
		for c := range parts {
			var cell string
			if c < len(cells) {
				cell = cells[c]
			}
			parts[c] = padCell(cell, colWidth[c], aligns[c])
		}
		// Drop trailing padding on the last column so rows carry no trailing space.
		parts[maxCols-1] = strings.TrimRight(parts[maxCols-1], " ")
		result = append(result, strings.Join(parts, "  "))
	}

	// No separator row present → insert a rule after the header.
	if !sepInserted && len(result) > 0 {
		result = append(result[:1], append([]string{strings.Repeat("─", totalWidth)}, result[1:]...)...)
	}
	return result
}
