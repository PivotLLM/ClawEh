// ClawEh
// License: MIT

package audit

import (
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/go-pdf/fpdf"
)

// Page geometry (mm) and type sizes (pt) for the A4 portrait layout.
const (
	marginLeft   = 15.0
	marginTop    = 20.0
	marginRight  = 15.0
	marginBottom = 18.0
	cellPad      = 1.2
	lineHeight   = 4.2
	fontBody     = 8.5
	fontNote     = 9.5
	fontH1       = 14.0
	fontH2       = 11.0
	fontTitle    = 18.0
	minColWidth  = 16.0
	maxColShare  = 0.6
)

// pdfDoc wraps an fpdf document with the state the renderer needs.
type pdfDoc struct {
	pdf   *fpdf.Fpdf
	tr    func(string) string // Unicode to the built-in font's code page
	width float64             // usable width between the margins
}

// RenderPDF renders the report as an A4 portrait PDF: a header with the
// product and version on every page, "Page x of y" in the footer, section
// headings, notes as paragraphs, and tables with wrapped cells that break
// cleanly across pages. Highlighted rows are lightly shaded.
func RenderPDF(r *Report, w io.Writer) error {
	pdf := fpdf.New("P", "mm", "A4", "")
	d := &pdfDoc{pdf: pdf, tr: pdf.UnicodeTranslatorFromDescriptor("")}
	pageW, _ := pdf.GetPageSize()
	d.width = pageW - marginLeft - marginRight

	pdf.SetTitle(r.Product+" security audit", true)
	pdf.SetAuthor(r.Product, true)
	pdf.SetCreator(r.Product+" "+r.Version, true)
	pdf.SetCreationDate(r.GeneratedAt)
	pdf.SetMargins(marginLeft, marginTop, marginRight)
	pdf.SetAutoPageBreak(true, marginBottom)
	pdf.SetCellMargin(0) // the table renderer applies its own cellPad
	pdf.AliasNbPages("")

	headerLeft := d.tr(r.Product + " " + r.Version)
	headerRight := d.tr("Generated " + r.GeneratedAt.Format(timeFormat))
	pdf.SetHeaderFunc(func() {
		pdf.SetY(8)
		pdf.SetFont("Helvetica", "", 9)
		pdf.SetTextColor(90, 90, 90)
		pdf.CellFormat(d.width/2, 5, headerLeft, "", 0, "L", false, 0, "")
		pdf.CellFormat(d.width/2, 5, headerRight, "", 0, "R", false, 0, "")
		pdf.SetDrawColor(180, 180, 180)
		pdf.Line(marginLeft, 14, marginLeft+d.width, 14)
		pdf.SetY(marginTop)
		pdf.SetTextColor(0, 0, 0)
	})
	pdf.SetFooterFunc(func() {
		pdf.SetY(-12)
		pdf.SetFont("Helvetica", "", 8)
		pdf.SetTextColor(90, 90, 90)
		pdf.CellFormat(0, 5, fmt.Sprintf("Page %d of {nb}", pdf.PageNo()), "", 0, "C", false, 0, "")
		pdf.SetTextColor(0, 0, 0)
	})

	pdf.AddPage()
	pdf.SetFont("Helvetica", "B", fontTitle)
	pdf.CellFormat(0, 9, d.tr(r.Product+" security audit"), "", 1, "L", false, 0, "")
	pdf.SetFont("Helvetica", "", fontNote)
	pdf.MultiCell(0, 5, d.tr(r.TagLine), "", "L", false)
	pdf.MultiCell(0, 5, d.tr("An inventory of what this instance can reach and do, derived from its configuration "+
		"and state on "+r.GeneratedAt.Format(timeFormat)+". No secret values appear in this document."), "", "L", false)
	pdf.Ln(3)

	for _, s := range r.Sections {
		d.section(s, false)
	}
	return pdf.Output(w)
}

// ensureSpace starts a new page when fewer than h mm remain on this one.
func (d *pdfDoc) ensureSpace(h float64) {
	_, pageH := d.pdf.GetPageSize()
	if d.pdf.GetY()+h > pageH-marginBottom {
		d.pdf.AddPage()
	}
}

func (d *pdfDoc) section(s Section, sub bool) {
	size, gap := fontH1, 8.0
	if sub {
		size, gap = fontH2, 6.0
	}
	// Keep a heading with the start of its content: a heading alone at the
	// foot of a page reads as an orphan.
	d.ensureSpace(gap + 40)
	d.pdf.Ln(gap / 2)
	d.pdf.SetFont("Helvetica", "B", size)
	d.pdf.CellFormat(0, gap, d.tr(s.Title), "", 1, "L", false, 0, "")
	d.pdf.SetFont("Helvetica", "", fontNote)
	for _, n := range s.Notes {
		d.pdf.MultiCell(0, 4.6, d.tr(n), "", "L", false)
		d.pdf.Ln(1)
	}
	for _, t := range s.Tables {
		d.table(t)
	}
	for _, ss := range s.Subsections {
		d.section(ss, true)
	}
}

// columnWidths shares the usable width between the columns. Each column is
// guaranteed its longest word (so headers and paths are not broken mid-word
// unless a single word is wider than the page share allows); the remaining
// width is distributed in proportion to how much more each column's longest
// content would like, capped at maxColShare of the page.
func (d *pdfDoc) columnWidths(t Table) []float64 {
	n := len(t.Columns)
	need := make([]float64, n) // floor: longest word
	want := make([]float64, n) // longest full cell, capped
	for i, c := range t.Columns {
		need[i] = d.pdf.GetStringWidth(d.tr(c)) + 2*cellPad + 1
	}
	for _, r := range t.Rows {
		for i := 0; i < n && i < len(r); i++ {
			for word := range strings.FieldsSeq(r[i]) {
				need[i] = max(need[i], d.pdf.GetStringWidth(d.tr(word))+2*cellPad+1)
			}
			want[i] = max(want[i], d.pdf.GetStringWidth(d.tr(r[i]))+2*cellPad+1)
		}
	}
	maxW := d.width * maxColShare
	needTotal, extraWant := 0.0, 0.0
	for i := range need {
		need[i] = min(max(need[i], minColWidth), maxW)
		want[i] = min(max(want[i], need[i]), maxW)
		needTotal += need[i]
		extraWant += want[i] - need[i]
	}
	widths := make([]float64, n)
	if needTotal >= d.width {
		for i := range need {
			widths[i] = need[i] / needTotal * d.width
		}
		return widths
	}
	spare := d.width - needTotal
	for i := range need {
		share := 1 / float64(n)
		if extraWant > 0 {
			share = (want[i] - need[i]) / extraWant
		}
		widths[i] = need[i] + spare*share
	}
	return widths
}

func (d *pdfDoc) rowHeight(cells []string, widths []float64) float64 {
	lines := 1
	for i, w := range widths {
		if i < len(cells) && cells[i] != "" {
			lines = max(lines, len(d.pdf.SplitLines([]byte(d.tr(cells[i])), w-2*cellPad)))
		}
	}
	return float64(lines)*lineHeight + 2*cellPad
}

func (d *pdfDoc) drawRow(cells []string, widths []float64, h float64, fill bool, bold bool) {
	pdf := d.pdf
	x, y := marginLeft, pdf.GetY()
	style := ""
	if bold {
		style = "B"
	}
	pdf.SetFont("Helvetica", style, fontBody)
	for i, w := range widths {
		if fill {
			pdf.Rect(x, y, w, h, "FD")
		} else {
			pdf.Rect(x, y, w, h, "D")
		}
		text := ""
		if i < len(cells) {
			text = d.tr(cells[i])
		}
		pdf.SetXY(x+cellPad, y+cellPad)
		pdf.MultiCell(w-2*cellPad, lineHeight, text, "", "L", false)
		x += w
	}
	pdf.SetXY(marginLeft, y+h)
}

func (d *pdfDoc) table(t Table) {
	if len(t.Columns) == 0 {
		return
	}
	pdf := d.pdf
	pdf.SetFont("Helvetica", "B", fontBody) // widths and the header height are measured in the wider face
	widths := d.columnWidths(t)
	headerH := d.rowHeight(t.Columns, widths)
	pdf.SetFont("Helvetica", "", fontBody)
	pdf.SetDrawColor(170, 170, 170)
	pdf.SetLineWidth(0.2)

	header := func() {
		pdf.SetFillColor(215, 215, 215)
		d.drawRow(t.Columns, widths, headerH, true, true)
	}
	firstRowH := 0.0
	if len(t.Rows) > 0 {
		firstRowH = d.rowHeight(t.Rows[0], widths)
	}
	d.ensureSpace(headerH + firstRowH + 8)
	if t.Caption != "" {
		pdf.Ln(2)
		pdf.SetFont("Helvetica", "B", fontNote)
		pdf.CellFormat(0, 5, d.tr(t.Caption), "", 1, "L", false, 0, "")
	}
	header()
	_, pageH := pdf.GetPageSize()
	for i, r := range t.Rows {
		h := d.rowHeight(r, widths)
		if pdf.GetY()+h > pageH-marginBottom && h < pageH-marginTop-marginBottom-headerH {
			pdf.AddPage()
			header()
		}
		hl := slices.Contains(t.Highlight, i)
		if hl {
			pdf.SetFillColor(228, 228, 228)
		}
		d.drawRow(r, widths, h, hl, false)
	}
	pdf.Ln(3)
}
