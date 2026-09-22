// ClawEh
// License: MIT

package audit

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

var wantSectionOrder = []string{
	"Identity", "Summary", "Network", "Providers and models", "Credentials and tokens", "Channels",
	"Agents", "External services", "Devices", "Data", "Scheduled activity",
}

func TestCollect_SectionOrder(t *testing.T) {
	cfg, env := fixtureConfig(t)
	r := Collect(t.Context(), cfg, env)
	if r.Product != "ClawEh" || r.Version != env.Version || !r.GeneratedAt.Equal(env.Now) {
		t.Errorf("report header = %q %q %v", r.Product, r.Version, r.GeneratedAt)
	}
	if len(r.Sections) != len(wantSectionOrder) {
		t.Fatalf("got %d sections, want %d", len(r.Sections), len(wantSectionOrder))
	}
	for i, want := range wantSectionOrder {
		if r.Sections[i].Title != want {
			t.Errorf("section %d = %q, want %q", i, r.Sections[i].Title, want)
		}
	}
}

func TestCollect_NilConfigDoesNotPanic(t *testing.T) {
	r := Collect(t.Context(), nil, Environment{})
	if len(r.Sections) != len(wantSectionOrder) || r.GeneratedAt.IsZero() {
		t.Errorf("nil config report: %d sections, generated %v", len(r.Sections), r.GeneratedAt)
	}
}

func TestRenderPDF(t *testing.T) {
	cfg, env := fixtureConfig(t)
	r := Collect(t.Context(), cfg, env)
	var buf bytes.Buffer
	if err := RenderPDF(r, &buf); err != nil {
		t.Fatalf("RenderPDF: %v", err)
	}
	if !bytes.HasPrefix(buf.Bytes(), []byte("%PDF-")) {
		t.Errorf("output does not start with %%PDF-: %q", buf.Bytes()[:8])
	}
	if buf.Len() < 8*1024 {
		t.Errorf("PDF is only %d bytes; expected a multi-page document", buf.Len())
	}
	txt := RenderText(r)
	for _, s := range r.Sections {
		contains(t, txt, "== "+s.Title+" ==", "text rendering section title")
		for _, sub := range s.Subsections {
			contains(t, txt, "-- "+sub.Title+" --", "text rendering subsection title")
		}
	}
	// A throwaway sample for a human to open: written only when asked for.
	if path := os.Getenv("AUDIT_SAMPLE_PDF"); path != "" {
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			t.Fatalf("write sample: %v", err)
		}
	}
}

func TestRenderText_NoSecrets(t *testing.T) {
	cfg, env := fixtureConfig(t)
	txt := RenderText(Collect(t.Context(), cfg, env))
	for _, s := range allSecrets {
		if strings.Contains(txt, s) {
			t.Errorf("secret %q appears in the rendered report", s)
		}
	}
	// Sanity: the things that must be named still are.
	contains(t, txt, "API providers", "providers table")
	contains(t, txt, "Authorization", "header name")
	contains(t, txt, "API_TOKEN", "mcp env name")
	contains(t, txt, "MY_SECRET_ENV", "cli env name")
}

func TestRenderText_HighlightMarker(t *testing.T) {
	r := &Report{Product: "P", Version: "1", Sections: []Section{{
		Title:  "S",
		Tables: []Table{{Caption: "T", Columns: []string{"A", "Bee"}, Rows: [][]string{{"x", "y"}, {"long cell", "z"}}, Highlight: []int{1}}},
	}}}
	txt := RenderText(r)
	contains(t, txt, "T:\n  A          Bee\n  ---------  ---\n  x          y\n! long cell  z\n", "aligned table")
}
