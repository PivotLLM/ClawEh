package web

import (
	"strings"
	"testing"
)

// A big HTML body with almost no readable text (JS-rendered / bot-challenge)
// gets the browser-tool hint; ordinary pages and non-HTML content do not.
func TestJSRenderedNote(t *testing.T) {
	tests := []struct {
		name      string
		extractor string
		textLen   int
		bodyLen   int
		wantNote  bool
	}{
		{"js shell: big body, tiny text", "text", 40, 50000, true},
		{"content page: plenty of text", "text", 5000, 50000, false},
		{"short page: small body", "text", 40, 300, false},
		{"json content", "json", 40, 50000, false},
		{"raw content", "raw", 40, 50000, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			note := jsRenderedNote(tt.extractor, tt.textLen, tt.bodyLen)
			if tt.wantNote && note == "" {
				t.Error("expected a note, got none")
			}
			if !tt.wantNote && note != "" {
				t.Errorf("expected no note, got %q", note)
			}
			if tt.wantNote && !strings.Contains(note, "browser tool") {
				t.Errorf("note should point to the browser tool, got %q", note)
			}
		})
	}
}
