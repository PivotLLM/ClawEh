// ClawEh
// License: MIT

package agent

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestToolResultCap_Bounds(t *testing.T) {
	tests := []struct {
		name   string
		window int
		want   int
	}{
		{"computed from window", 128000, 128000 / 4 * 4}, // 25% of tokens at 4 chars/token
		{"zero window hits floor", 0, toolResultCapFloor},
		{"small window hits floor", 8000, toolResultCapFloor},
		{"huge window hits ceiling", 2_000_000, toolResultCapCeiling},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := toolResultCap(tt.window); got != tt.want {
				t.Fatalf("toolResultCap(%d) = %d, want %d", tt.window, got, tt.want)
			}
		})
	}
}

func TestCapToolResult_TruncatesWithMarker(t *testing.T) {
	const window = 128000
	limit := toolResultCap(window)
	content := strings.Repeat("a", limit+1000)

	got := capToolResult(content, false, window)

	wantMarker := fmt.Sprintf("\n[output truncated: kept %d of %d characters. "+
		"Use the tool's paging/range options or a narrower query to see more.]", limit, len(content))
	if !strings.HasSuffix(got, wantMarker) {
		t.Fatalf("marker missing or wrong; tail=%q", got[len(got)-120:])
	}
	if head := strings.TrimSuffix(got, wantMarker); head != content[:limit] {
		t.Fatalf("head not preserved: len=%d want %d", len(head), limit)
	}
}

func TestCapToolResult_UnderCapUntouched(t *testing.T) {
	content := strings.Repeat("b", toolResultCap(128000))
	if got := capToolResult(content, false, 128000); got != content {
		t.Fatal("content at the cap must not be modified")
	}
}

func TestCapToolResult_DoesNotSplitRune(t *testing.T) {
	limit := toolResultCap(0)
	// Place a 3-byte rune so that the cap lands on its middle byte.
	content := strings.Repeat("x", limit-1) + strings.Repeat("€", 3)
	got := capToolResult(content, false, 0)
	head, _, found := strings.Cut(got, "\n[output truncated")
	if !found {
		t.Fatalf("no truncation marker in %q", got)
	}
	if !utf8.ValidString(head) {
		t.Fatal("kept head is not valid UTF-8")
	}
	if len(head) != limit-1 {
		t.Fatalf("head len = %d, want %d (rune boundary)", len(head), limit-1)
	}
}

func TestCapToolResult_ErrorTextNeverBelowFourKiB(t *testing.T) {
	// The floor is above the error minimum, so an error at the minimum is
	// untouched for every window size.
	errText := strings.Repeat("e", toolErrorMinChars)
	for _, window := range []int{0, 1, 4000, 128000} {
		if got := capToolResult(errText, true, window); got != errText {
			t.Fatalf("window %d: error text of %d bytes was truncated", window, len(errText))
		}
	}
	if toolResultCapFloor < toolErrorMinChars {
		t.Fatalf("floor %d below error minimum %d", toolResultCapFloor, toolErrorMinChars)
	}
	// An oversized error is still capped.
	big := strings.Repeat("e", toolResultCap(0)+1)
	if got := capToolResult(big, true, 0); !strings.Contains(got, "[output truncated") {
		t.Fatal("oversized error text was not capped")
	}
}
