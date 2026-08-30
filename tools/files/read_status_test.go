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

	t.Run("read_bytes rejects line params", func(t *testing.T) {
		res := tool.Execute(ctx, map[string]any{"path": f, "start_line": 5})
		if !res.IsError || !strings.Contains(res.ForLLM, "file_read_lines") {
			t.Fatalf("expected rejection pointing at file_read_lines, got: %s", res.ForLLM)
		}
	})

	t.Run("read_lines rejects byte params", func(t *testing.T) {
		lt := NewReadLinesTool(tmpDir, false, MaxReadFileSize)
		res := lt.Execute(ctx, map[string]any{"path": f, "offset": 10})
		if !res.IsError || !strings.Contains(res.ForLLM, "file_read_bytes") {
			t.Fatalf("expected rejection pointing at file_read_bytes, got: %s", res.ForLLM)
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

func TestListDir_Recursive(t *testing.T) {
	ws := t.TempDir()
	base := filepath.Join(ws, "files")
	os.MkdirAll(filepath.Join(base, "sub", "deep"), 0o755)
	os.WriteFile(filepath.Join(base, "top.md"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(base, "sub", "a.md"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(base, "sub", "deep", "b.md"), []byte("x"), 0o644)
	os.MkdirAll(filepath.Join(base, ".git"), 0o755)
	os.WriteFile(filepath.Join(base, ".git", "config"), []byte("x"), 0o644)

	tool := NewListDirTool(ws, true) // sandboxed at ws
	out := tool.Execute(context.Background(), map[string]any{"path": "files", "recursive": true}).ForLLM
	for _, want := range []string{"FILE: files/top.md", "DIR:  files/sub", "FILE: files/sub/a.md", "FILE: files/sub/deep/b.md"} {
		if !contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if contains(out, ".git") {
		t.Fatalf("hidden dirs should be skipped:\n%s", out)
	}

	// Non-recursive stays one level.
	flat := tool.Execute(context.Background(), map[string]any{"path": "files"}).ForLLM
	if contains(flat, "a.md") {
		t.Fatalf("non-recursive must not descend:\n%s", flat)
	}
}
