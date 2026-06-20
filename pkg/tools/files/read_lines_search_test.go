package files

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadFile_LineMode(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "outline.md")
	os.WriteFile(f, []byte("L1\nL2\nL3\nL4\nL5\n"), 0o644)

	tool := NewReadFileTool("", false, MaxReadFileSize)
	res := tool.Execute(context.Background(), map[string]any{
		"path": f, "start_line": 2, "line_count": 2,
	})
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.ForLLM)
	}
	// Numbered lines 2 and 3, not 1 or 4.
	for _, want := range []string{"2: L2", "3: L3"} {
		if !strings.Contains(res.ForLLM, want) {
			t.Fatalf("missing %q in:\n%s", want, res.ForLLM)
		}
	}
	if strings.Contains(res.ForLLM, "1: L1") || strings.Contains(res.ForLLM, "4: L4") {
		t.Fatalf("line mode returned out-of-range lines:\n%s", res.ForLLM)
	}
	// Tells the caller how to continue.
	if !strings.Contains(res.ForLLM, "start_line=4") {
		t.Fatalf("expected a continue hint:\n%s", res.ForLLM)
	}
}

func TestSearchFiles_FindsMatches(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "sub"), 0o755)
	os.WriteFile(filepath.Join(dir, "a.md"), []byte("alpha\nChapter One\nbeta\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "sub", "b.md"), []byte("gamma\nchapter one again\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "bin"), []byte{0, 1, 2, 'C', 'h', 'a', 'p', 't', 'e', 'r'}, 0o644)

	tool := NewSearchFilesTool("", false)

	// Literal, case-insensitive: matches both files (recursive), skips binary.
	res := tool.Execute(context.Background(), map[string]any{"query": "chapter one", "path": dir})
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.ForLLM)
	}
	if !strings.Contains(res.ForLLM, "a.md:2:") || !strings.Contains(res.ForLLM, "b.md:2:") {
		t.Fatalf("expected matches in both files with line numbers:\n%s", res.ForLLM)
	}
	if strings.Contains(res.ForLLM, "bin:") {
		t.Fatalf("binary file should be skipped:\n%s", res.ForLLM)
	}

	// No matches → clear message, not an error.
	res = tool.Execute(context.Background(), map[string]any{"query": "zzz-nope", "path": dir})
	if res.IsError || !strings.Contains(res.ForLLM, "No matches") {
		t.Fatalf("expected a no-matches message, got: %s", res.ForLLM)
	}

	// Regex mode.
	res = tool.Execute(context.Background(), map[string]any{"query": "^beta$", "path": dir, "regex": true})
	if res.IsError || !strings.Contains(res.ForLLM, "a.md:3:") {
		t.Fatalf("regex search failed:\n%s", res.ForLLM)
	}
}
