// ClawEh
// License: MIT

package files

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/tools"
)

// slowPattern is a valid glob whose brace groups make matching each name
// take seconds: it must be refused before any walk.
var slowPattern = strings.Repeat("{a,a,a}", 16) + "b"

// A pattern or include that is too long or has too many brace groups is
// refused at once with a short sentence; one at the limits is accepted.
func TestGlobPattern_CostLimits(t *testing.T) {
	ws := t.TempDir()
	mustWrite(t, filepath.Join(ws, "files", "a.txt"), "needle\n")
	ctx := context.Background()
	glob := NewGlobTool(ws, true)
	search := NewSearchLinesTool(ws, true)
	for name, tc := range map[string]struct {
		pattern string
		want    string
	}{
		"too long":    {strings.Repeat("a", maxGlobPatternChars+1), "is longer than 512 characters"},
		"many braces": {slowPattern, "has more than 10 {…} groups"},
	} {
		start := time.Now()
		res := glob.Execute(ctx, map[string]any{"pattern": tc.pattern, "path": "files"})
		if !res.IsError || !strings.Contains(res.ForLLM, "pattern "+tc.want) || !tools.IsExpectedRefusal(res.Err) {
			t.Errorf("glob %s: %v %s", name, res.IsError, res.ForLLM)
		}
		res = search.Execute(ctx, map[string]any{"query": "needle", "path": "files", "include": tc.pattern})
		if !res.IsError || !strings.Contains(res.ForLLM, "include "+tc.want) {
			t.Errorf("include %s: %v %s", name, res.IsError, res.ForLLM)
		}
		if d := time.Since(start); d > 2*time.Second {
			t.Errorf("%s took %v: refused too late", name, d)
		}
	}
	ok := strings.Repeat("{a,b}", maxGlobBraceGroups) + strings.Repeat("x", maxGlobPatternChars-5*maxGlobBraceGroups)
	if res := glob.Execute(ctx, map[string]any{"pattern": ok, "path": "files"}); res.IsError {
		t.Errorf("a pattern at the limits was refused: %s", res.ForLLM)
	}
}

// A cancelled turn stops file_glob and file_search_lines (both modes).
func TestGlobAndSearch_Cancelled(t *testing.T) {
	ws := t.TempDir()
	for i := range 50 {
		mustWrite(t, filepath.Join(ws, "files", fmt.Sprintf("d%02d", i), "a.txt"), "needle\n")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for name, res := range map[string]*tools.ToolResult{
		"glob":         NewGlobTool(ws, true).Execute(ctx, map[string]any{"pattern": "**", "path": "files"}),
		"search lines": NewSearchLinesTool(ws, true).Execute(ctx, map[string]any{"query": "needle", "path": "files"}),
		"search bytes": NewSearchBytesTool(ws, true).Execute(ctx, map[string]any{"query": "needle", "path": "files"}),
	} {
		if !res.IsError || !strings.Contains(res.ForLLM, "stopped") || !strings.Contains(res.ForLLM, "context canceled") {
			t.Errorf("%s after cancel: %v %s", name, res.IsError, res.ForLLM)
		}
	}
}

// file_glob stops at its entry limit and says so.
func TestGlob_EntryLimit(t *testing.T) {
	ws := t.TempDir()
	for i := range 10 {
		mustWrite(t, filepath.Join(ws, "files", fmt.Sprintf("f%d.txt", i)), "x")
	}
	old := globEntryLimit
	globEntryLimit = 4
	t.Cleanup(func() { globEntryLimit = old })
	res := NewGlobTool(ws, true).Execute(context.Background(), map[string]any{"pattern": "*.txt", "path": "files"})
	if res.IsError || len(resultPaths(res.ForLLM)) != 4 || !strings.Contains(res.ForLLM, "[Stopped after 4 entries") {
		t.Errorf("entry limit: %s", res.ForLLM)
	}
	res = NewGlobTool(ws, true).Execute(context.Background(), map[string]any{"pattern": "*.md", "path": "files"})
	if !strings.Contains(res.ForLLM, "No files match") || !strings.Contains(res.ForLLM, "[Stopped after 4 entries") {
		t.Errorf("entry limit with no match: %s", res.ForLLM)
	}
}

// A .gitignore that is a link, a FIFO or larger than 64 KiB is not read.
func TestSearchLines_GitignoreFileKinds(t *testing.T) {
	setup := func(t *testing.T) string {
		t.Helper()
		ws := searchTree(t, map[string]string{"skip.log": "needle\n", "keep.txt": "needle\n"})
		mustWrite(t, filepath.Join(ws, "rules"), "*.log\n")
		return ws
	}
	ignored := func(t *testing.T, ws string) bool {
		t.Helper()
		out := searchLines(t, ws, map[string]any{"query": "needle"})
		if !strings.Contains(out, "files/keep.txt:") {
			t.Fatalf("keep.txt not searched:\n%s", out)
		}
		return !strings.Contains(out, "files/skip.log:")
	}

	t.Run("regular file is read", func(t *testing.T) {
		ws := setup(t)
		mustWrite(t, filepath.Join(ws, "files", ".gitignore"), "*.log\n")
		if !ignored(t, ws) {
			t.Error("a regular .gitignore was not applied")
		}
	})
	t.Run("link is skipped", func(t *testing.T) {
		ws := setup(t)
		if err := os.Symlink(filepath.Join(ws, "rules"), filepath.Join(ws, "files", ".gitignore")); err != nil {
			t.Fatal(err)
		}
		if ignored(t, ws) {
			t.Error("a .gitignore link was followed")
		}
	})
	t.Run("FIFO is skipped", func(t *testing.T) {
		ws := setup(t)
		if err := syscall.Mkfifo(filepath.Join(ws, "files", ".gitignore"), 0o600); err != nil {
			t.Skipf("mkfifo: %v", err)
		}
		done := make(chan string, 1)
		go func() {
			done <- NewSearchLinesTool(ws, true).Execute(context.Background(),
				map[string]any{"query": "needle", "path": "files"}).ForLLM
		}()
		select {
		case out := <-done:
			if !strings.Contains(out, "files/skip.log:") || !strings.Contains(out, "files/keep.txt:") {
				t.Errorf("a FIFO .gitignore was read:\n%s", out)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the search blocked on a FIFO .gitignore")
		}
	})
	t.Run("oversized is skipped whole", func(t *testing.T) {
		ws := setup(t)
		big := "*.log\n" + strings.Repeat("# padding\n", maxGitignoreBytes/10+1)
		mustWrite(t, filepath.Join(ws, "files", ".gitignore"), big)
		if ignored(t, ws) {
			t.Error("a .gitignore over 64 KiB was applied")
		}
	})
}

// With context, one result shows at most maxSearchOutputLines lines, cut with
// a note; the matches it reports are the ones shown.
func TestSearchLines_ContextOutputCap(t *testing.T) {
	var b strings.Builder
	for i := range 3000 {
		if i%25 == 0 {
			fmt.Fprintf(&b, "hit %d\n", i)
		} else {
			fmt.Fprintf(&b, "line %d\n", i)
		}
	}
	files := map[string]string{}
	for i := range 3 {
		files[fmt.Sprintf("f%d.txt", i)] = b.String()
	}
	ws := searchTree(t, files)
	out := searchLines(t, ws, map[string]any{"query": "hit", "context": 10, "max_results": 200})
	head, body, _ := strings.Cut(out, "\n\n")
	shown := 0
	matches := 0
	for l := range strings.SplitSeq(body, "\n") {
		if l == "--" {
			continue
		}
		shown++
		if strings.Contains(l, ".txt:") {
			matches++
		}
	}
	if shown > maxSearchOutputLines {
		t.Errorf("shown %d lines, cap %d", shown, maxSearchOutputLines)
	}
	if !strings.Contains(head, "[Showing the first 2000 lines") {
		t.Errorf("no output cap note:\n%s", head)
	}
	if !strings.HasPrefix(head, fmt.Sprintf("%d match(es)", matches)) {
		t.Errorf("header counts other than the %d matches shown:\n%s", matches, head)
	}
	if !strings.Contains(fmt.Sprint((&SearchFilesTool{}).Parameters()["properties"]), "at most 2000 lines") {
		t.Error("the context description does not mention the cap")
	}
}

// output=count stops at max_results files with the usual note.
func TestSearchLines_CountCap(t *testing.T) {
	ws := searchTree(t, map[string]string{"a.txt": "x\n", "b.txt": "x\nx\n", "c.txt": "x\n"})
	out := searchLines(t, ws, map[string]any{"query": "x", "output": "count", "max_results": 2})
	if !strings.HasSuffix(out, "\n\nfiles/a.txt: 1\nfiles/b.txt: 2") || !strings.Contains(out, "[Showing the first 2") {
		t.Errorf("output=count with max_results:\n%s", out)
	}
}
