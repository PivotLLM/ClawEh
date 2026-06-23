// ClawEh
// License: MIT

package files

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReadStatusBlock verifies the explicit, after-content status block: the
// content precedes the block, the next offset is computed, chunk numbers are
// shown, and the "do not call without offset" warning is present.
func TestReadStatusBlock(t *testing.T) {
	tmpDir := t.TempDir()
	f := filepath.Join(tmpDir, "doc.txt")
	if err := os.WriteFile(f, []byte("abcdefghijklmnopqrstuvwxyz"), 0o644); err != nil { // 26 bytes
		t.Fatal(err)
	}
	tool := NewReadFileTool(tmpDir, false, MaxReadFileSize)
	ctx := context.Background()

	t.Run("truncated chunk", func(t *testing.T) {
		res := tool.Execute(ctx, map[string]any{"path": f, "offset": 0, "length": 10})
		out := res.ForLLM
		// Content comes before the status block (recency position).
		ci, si := strings.Index(out, "abcdefghij"), strings.Index(out, "=== FILE READ STATUS ===")
		if ci < 0 || si < 0 || ci > si {
			t.Fatalf("content must precede the status block\n%s", out)
		}
		for _, want := range []string{
			"Total size: 26 bytes",
			"(chunk 1 of 3)",
			"TRUNCATED",
			"ACTION REQUIRED",
			"offset=10)",
			"Do NOT call file_read_bytes again without offset=10",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("missing %q in:\n%s", want, out)
			}
		}
	})

	t.Run("final chunk is complete", func(t *testing.T) {
		res := tool.Execute(ctx, map[string]any{"path": f, "offset": 20, "length": 10})
		out := res.ForLLM
		if !strings.Contains(out, "Status: COMPLETE") || !strings.Contains(out, "END OF FILE") {
			t.Errorf("final chunk should be COMPLETE/END OF FILE:\n%s", out)
		}
		if strings.Contains(out, "ACTION REQUIRED") || strings.Contains(out, "TRUNCATED") {
			t.Errorf("final chunk should not ask to continue:\n%s", out)
		}
	})
}
