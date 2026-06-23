// ClawEh
// License: MIT

package agent

import (
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/llmcontext"
)

func TestSummarizeEvictions(t *testing.T) {
	t.Run("single resource collapses to one line with count", func(t *testing.T) {
		var ev []llmcontext.EvictionEvent
		for i := 0; i < 8; i++ {
			ev = append(ev, llmcontext.EvictionEvent{Tool: "file_read", Resource: "files/novels/outline.md", Bytes: 65692, Reason: "superseded"})
		}
		got := summarizeEvictions(ev)
		want := "[Context: evicted 8 read(s), freed 513 KB — files/novels/outline.md ×8]"
		if got != want {
			t.Fatalf("got %q\nwant %q", got, want)
		}
	})

	t.Run("multiple resources ordered by count", func(t *testing.T) {
		ev := []llmcontext.EvictionEvent{
			{Resource: "a.md", Bytes: 100},
			{Resource: "b.md", Bytes: 100},
			{Resource: "a.md", Bytes: 100},
		}
		got := summarizeEvictions(ev)
		if !strings.Contains(got, "a.md ×2") || !strings.Contains(got, "b.md ×1") {
			t.Fatalf("missing per-resource counts: %q", got)
		}
		if !strings.HasPrefix(got, "[Context: evicted 3 read(s), freed 300 B") {
			t.Fatalf("bad header/bytes: %q", got)
		}
	})

	t.Run("caps to top resources with +N more", func(t *testing.T) {
		ev := []llmcontext.EvictionEvent{
			{Resource: "a", Bytes: 1}, {Resource: "b", Bytes: 1},
			{Resource: "c", Bytes: 1}, {Resource: "d", Bytes: 1}, {Resource: "e", Bytes: 1},
		}
		got := summarizeEvictions(ev)
		if !strings.Contains(got, "+2 more") {
			t.Fatalf("expected '+2 more' for 5 distinct resources: %q", got)
		}
	})
}
