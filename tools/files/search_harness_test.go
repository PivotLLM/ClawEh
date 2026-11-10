// ClawEh
// License: MIT

package files

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A literal query ignores case; a regular expression is case-sensitive as
// written and can ask for case-insensitivity itself; an invalid one is
// refused.
func TestSearchLines_CaseRules(t *testing.T) {
	ws := searchTree(t, map[string]string{"a.txt": "needle\nNEEDLE\nNeedle\n"})
	for _, tc := range []struct {
		query string
		regex bool
		want  []string
	}{
		{"NeEdLe", false, []string{":1:", ":2:", ":3:"}},
		{"Needle", true, []string{":3:"}},
		{"(?i)needle", true, []string{":1:", ":2:", ":3:"}},
		{"N.*E$", true, []string{":2:"}},
	} {
		out := searchLines(t, ws, map[string]any{"query": tc.query, "regex": tc.regex})
		for _, line := range []string{":1:", ":2:", ":3:"} {
			if want := strings.Contains(strings.Join(tc.want, ""), line); strings.Contains(out, "files/a.txt"+line) != want {
				t.Errorf("query %q regex=%v: line %s matched=%v, want %v:\n%s", tc.query, tc.regex, line, !want, want, out)
			}
		}
	}
	// A literal query with regular-expression characters matches them as text.
	ws = searchTree(t, map[string]string{"b.txt": "a.b(c)\naxb(c)\n"})
	out := searchLines(t, ws, map[string]any{"query": "a.b(c)"})
	if !strings.Contains(out, "files/b.txt:1:") || strings.Contains(out, "files/b.txt:2:") {
		t.Errorf("literal query treated as a pattern:\n%s", out)
	}
	res := NewSearchLinesTool(ws, true).Execute(context.Background(), map[string]any{"query": "(", "regex": true, "path": "files"})
	if !res.IsError || !strings.Contains(res.ForLLM, "invalid regex") {
		t.Errorf("invalid regex: %v %s", res.IsError, res.ForLLM)
	}
}

// Files over 1 MB and binary files are skipped in every output mode; a file
// of exactly 1 MB is searched.
func TestSearchLines_SkipsLargeAndBinary(t *testing.T) {
	limit := "needle\n" + strings.Repeat("x", maxSearchFileBytes-len("needle\n"))
	ws := searchTree(t, map[string]string{
		"limit.txt": limit,
		"big.txt":   limit + "y",
		"bin.dat":   "needle\x00\n",
		"ok.txt":    "needle\n",
	})
	for _, output := range []string{"lines", "files", "count"} {
		out := searchLines(t, ws, map[string]any{"query": "needle", "output": output})
		for _, want := range []string{"files/limit.txt", "files/ok.txt"} {
			if !strings.Contains(out, want) {
				t.Errorf("output=%s lacks %s:\n%s", output, want, out)
			}
		}
		for _, bad := range []string{"big.txt", "bin.dat"} {
			if strings.Contains(out, bad) {
				t.Errorf("output=%s searched %s:\n%s", output, bad, out)
			}
		}
	}
}

// file_search_lines reaches what the other reading tools reach: an allowed
// folder (read-only too) by absolute path, as a folder or a single file;
// not a folder outside, the workspace root or a parent, and not through a
// link out of files/ or out of an allowed folder.
func TestSearchLines_PermissionRules(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs file permissions that bind the test account")
	}
	ws := t.TempDir()
	mustWrite(t, filepath.Join(ws, "files", "in.txt"), "needle in\n")
	mustWrite(t, filepath.Join(ws, "AGENTS.md"), "needle root\n")
	src := t.TempDir()
	mustWrite(t, filepath.Join(src, "pkg", "a.go"), "needle src\n")
	ro := t.TempDir()
	mustWrite(t, filepath.Join(ro, "b.go"), "needle ro\n")
	outside := t.TempDir()
	mustWrite(t, filepath.Join(outside, "secret.txt"), "needle secret\n")
	for _, dir := range []string{filepath.Join(ws, "files"), src} {
		if err := os.Symlink(outside, filepath.Join(dir, "out")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(dir, "secret.txt")); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(ro, 0o700); err != nil {
			t.Error(err)
		}
	})
	allowFolders(t, ws, src, ro)
	defaultScope(t)
	tool := NewSearchLinesTool(ws, true)
	ctx := context.Background()

	for path, want := range map[string]string{
		src:                               filepath.Join(src, "pkg", "a.go") + ":1: needle src",
		filepath.Join(src, "pkg", "a.go"): filepath.Join(src, "pkg", "a.go") + ":1: needle src",
		ro:                                filepath.Join(ro, "b.go") + ":1: needle ro",
		"files":                           "files/in.txt:1: needle in",
		filepath.Join(ws, "files"):        filepath.Join(ws, "files", "in.txt") + ":1: needle in",
	} {
		res := tool.Execute(ctx, map[string]any{"query": "needle", "path": path})
		if res.IsError || !strings.Contains(res.ForLLM, want) {
			t.Errorf("search %s: %v %s", path, res.IsError, res.ForLLM)
		}
		if strings.Contains(res.ForLLM, "secret") {
			t.Errorf("search %s followed a link out:\n%s", path, res.ForLLM)
		}
	}
	for _, path := range []string{
		".", "..", "AGENTS.md", outside, filepath.Join(outside, "secret.txt"),
		"files/out", "files/secret.txt", filepath.Join(src, "out"), filepath.Join(src, "secret.txt"),
	} {
		res := tool.Execute(ctx, map[string]any{"query": "needle", "path": path})
		if !res.IsError || strings.Contains(res.ForLLM, "needle") {
			t.Errorf("search %s: want a refusal, got %v %s", path, res.IsError, res.ForLLM)
		}
	}
	// Without a path, the allowed folders outside the workspace are not
	// searched and the links out are not followed.
	res := tool.Execute(ctx, map[string]any{"query": "needle"})
	if res.IsError || !strings.Contains(res.ForLLM, "files/in.txt:1:") || strings.Contains(res.ForLLM, "secret") ||
		strings.Contains(res.ForLLM, "needle src") || strings.Contains(res.ForLLM, "needle root") {
		t.Errorf("search without a path:\n%s", res.ForLLM)
	}
}
