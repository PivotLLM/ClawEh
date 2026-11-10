// ClawEh
// License: MIT

package files

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// searchTree writes files under <ws>/files and returns the workspace.
func searchTree(t *testing.T, files map[string]string) string {
	t.Helper()
	ws := t.TempDir()
	for rel, content := range files {
		mustWrite(t, filepath.Join(ws, "files", rel), content)
	}
	return ws
}

func searchLines(t *testing.T, ws string, args map[string]any) string {
	t.Helper()
	if _, ok := args["path"]; !ok {
		args["path"] = "files"
	}
	res := NewSearchLinesTool(ws, true).Execute(context.Background(), args)
	if res.IsError {
		t.Fatalf("search %v: %s", args, res.ForLLM)
	}
	return res.ForLLM
}

// include keeps only files whose path matches: a pattern without "/" matches
// the file name in any folder, one with "/" the path below the searched folder.
func TestSearchLines_Include(t *testing.T) {
	ws := searchTree(t, map[string]string{
		"main.go":         "needle\n",
		"pkg/util.go":     "needle\n",
		"pkg/util.ts":     "needle\n",
		"web/src/app.ts":  "needle\n",
		"web/lib/deep.ts": "needle\n",
	})
	out := searchLines(t, ws, map[string]any{"query": "needle", "include": "*.go"})
	for _, want := range []string{"files/main.go:1:", "files/pkg/util.go:1:"} {
		if !strings.Contains(out, want) {
			t.Errorf("*.go lacks %s:\n%s", want, out)
		}
	}
	if strings.Contains(out, ".ts") {
		t.Errorf("*.go matched a .ts file:\n%s", out)
	}
	out = searchLines(t, ws, map[string]any{"query": "needle", "include": "web/**/*.ts"})
	if !strings.Contains(out, "files/web/src/app.ts:1:") || !strings.Contains(out, "files/web/lib/deep.ts:1:") || strings.Contains(out, "pkg/") {
		t.Errorf("web/**/*.ts:\n%s", out)
	}
	out = searchLines(t, ws, map[string]any{"query": "needle", "include": "web/*.ts"})
	if !strings.Contains(out, "No matches") {
		t.Errorf("web/*.ts must not reach web/src:\n%s", out)
	}
	// A single file is matched by its name, or by the path as given for a
	// pattern with "/".
	out = searchLines(t, ws, map[string]any{"query": "needle", "path": "files/main.go", "include": "*.ts"})
	if !strings.Contains(out, "No matches") {
		t.Errorf("single file filtered by include:\n%s", out)
	}
	out = searchLines(t, ws, map[string]any{"query": "needle", "path": "files/pkg/util.go", "include": "files/pkg/*.go"})
	if !strings.Contains(out, "files/pkg/util.go:1:") {
		t.Errorf("single file with a path include:\n%s", out)
	}
	if res := NewSearchLinesTool(ws, true).Execute(context.Background(), map[string]any{"query": "x", "path": "files", "include": "[a"}); !res.IsError {
		t.Error("an invalid include pattern was accepted")
	}
}

// context shows the lines around each match, merges overlapping runs, keeps
// indentation, and is capped at 10.
func TestSearchLines_Context(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 40; i++ {
		switch i {
		case 5, 7, 30:
			fmt.Fprintf(&b, "    hit %d\n", i)
		default:
			fmt.Fprintf(&b, "line %d\n", i)
		}
	}
	ws := searchTree(t, map[string]string{"a.txt": b.String()})
	out := searchLines(t, ws, map[string]any{"query": "hit", "context": 1})
	want := "files/a.txt-4- line 4\nfiles/a.txt:5:     hit 5\nfiles/a.txt-6- line 6\nfiles/a.txt:7:     hit 7\nfiles/a.txt-8- line 8\n--\n" +
		"files/a.txt-29- line 29\nfiles/a.txt:30:     hit 30\nfiles/a.txt-31- line 31"
	if !strings.HasSuffix(out, want) {
		t.Errorf("context 1:\n%s\nwant suffix:\n%s", out, want)
	}
	if !strings.HasPrefix(out, "3 match(es)") {
		t.Errorf("context counts matches, not lines:\n%s", out)
	}

	out = searchLines(t, ws, map[string]any{"query": "hit 30", "context": 50})
	if !strings.Contains(out, "files/a.txt-20- line 20") || strings.Contains(out, "files/a.txt-19-") ||
		!strings.Contains(out, "files/a.txt-40- line 40") {
		t.Errorf("context is not capped at 10:\n%s", out)
	}

	// At the start and end of a file the block stops at the file.
	out = searchLines(t, ws, map[string]any{"query": "line 1$", "regex": true, "context": 3})
	if !strings.Contains(out, "files/a.txt:1: line 1\nfiles/a.txt-2- line 2") {
		t.Errorf("context at the top:\n%s", out)
	}

	// max_results counts matches with context too; a match past the limit
	// inside a block is still marked as a match.
	out = searchLines(t, ws, map[string]any{"query": "hit", "context": 2, "max_results": 1})
	if !strings.Contains(out, "files/a.txt:7:     hit 7") || strings.Contains(out, ":30:") ||
		!strings.Contains(out, "Showing the first 1") {
		t.Errorf("max_results with context:\n%s", out)
	}
}

// output=files lists each matching file once; output=count gives the
// matching lines per file; lines stays today's output.
func TestSearchLines_Output(t *testing.T) {
	ws := searchTree(t, map[string]string{
		"a.txt":     "x\nx\nx\n",
		"b/b.txt":   "x\n",
		"c/none.md": "nothing\n",
	})
	out := searchLines(t, ws, map[string]any{"query": "x", "output": "files"})
	if !strings.HasPrefix(out, `2 file(s) match "x" in files:`) || !strings.HasSuffix(out, "\n\nfiles/a.txt\nfiles/b/b.txt") {
		t.Errorf("output=files:\n%s", out)
	}
	out = searchLines(t, ws, map[string]any{"query": "x", "output": "count"})
	if !strings.HasSuffix(out, "\n\nfiles/a.txt: 3\nfiles/b/b.txt: 1") {
		t.Errorf("output=count:\n%s", out)
	}
	out = searchLines(t, ws, map[string]any{"query": "x", "output": "files", "max_results": 1})
	if !strings.HasSuffix(out, "\n\nfiles/a.txt") || !strings.Contains(out, "Showing the first 1") {
		t.Errorf("output=files with max_results:\n%s", out)
	}
	lines := searchLines(t, ws, map[string]any{"query": "x", "output": "lines"})
	plain := searchLines(t, ws, map[string]any{"query": "x"})
	if lines != plain {
		t.Errorf("output=lines differs from the default:\n%s\n---\n%s", lines, plain)
	}
	want := `4 match(es) for "x" in files (line numbers):` + "\nRead from a match with file_read_lines(start_line=<line>).\n\n" +
		"files/a.txt:1: x\nfiles/a.txt:2: x\nfiles/a.txt:3: x\nfiles/b/b.txt:1: x"
	if plain != want {
		t.Errorf("default output changed:\n%s\nwant:\n%s", plain, want)
	}
	if res := NewSearchLinesTool(ws, true).Execute(context.Background(), map[string]any{"query": "x", "path": "files", "output": "paths"}); !res.IsError {
		t.Error("an unknown output was accepted")
	}
}

// .gitignore files in the searched tree are honoured: the root's and nested
// ones (scoped to their folder), negation, and ignored folders.
func TestSearchLines_Gitignore(t *testing.T) {
	ws := searchTree(t, map[string]string{
		".gitignore":          "*.log\nbuild/\n!keep.log\n# a comment\n\n",
		"app.go":              "needle\n",
		"debug.log":           "needle\n",
		"keep.log":            "needle\n",
		"build/out.go":        "needle\n",
		"pkg/.gitignore":      "gen_*.go\r\n",
		"pkg/gen_a.go":        "needle\n",
		"pkg/real.go":         "needle\n",
		"pkg/sub/gen_b.go":    "needle\n",
		"other/gen_c.go":      "needle\n",
		"node_modules/x.js":   "needle\n",
		"docs/.gitignore":     "/top.md\n",
		"docs/top.md":         "needle\n",
		"docs/inner/top.md":   "needle\n",
		"node_modules/.keep":  "",
		"vendor/.gitignore":   "*\n!*.go\n",
		"vendor/lib.go":       "needle\n",
		"vendor/lib.c":        "needle\n",
		"ignored.log.go.keep": "needle\n",
	})
	mustWrite(t, filepath.Join(ws, "files", ".gitignore"), "*.log\nbuild/\n!keep.log\n# a comment\n\nnode_modules\n")
	out := searchLines(t, ws, map[string]any{"query": "needle", "max_results": 200})
	for _, want := range []string{
		"files/app.go:", "files/keep.log:", "files/pkg/real.go:", "files/other/gen_c.go:",
		"files/docs/inner/top.md:", "files/vendor/lib.go:", "files/ignored.log.go.keep:",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s:\n%s", want, out)
		}
	}
	for _, bad := range []string{
		"files/debug.log:", "files/build/", "files/pkg/gen_a.go", "files/pkg/sub/gen_b.go",
		"node_modules", "files/docs/top.md:", "files/vendor/lib.c",
	} {
		if strings.Contains(out, bad) {
			t.Errorf("ignored file searched (%s):\n%s", bad, out)
		}
	}

	// file_search_bytes is unchanged: it does not read .gitignore.
	res := NewSearchBytesTool(ws, true).Execute(context.Background(), map[string]any{"query": "needle", "path": "files", "max_results": 200})
	if !strings.Contains(res.ForLLM, "files/debug.log: bytes") {
		t.Errorf("file_search_bytes honoured .gitignore:\n%s", res.ForLLM)
	}
}

// file_search_bytes publishes none of the line-mode parameters.
func TestSearchBytes_ParametersUnchanged(t *testing.T) {
	props, ok := (&SearchFilesTool{byteMode: true}).Parameters()["properties"].(map[string]any)
	if !ok {
		t.Fatal("file_search_bytes has no properties")
	}
	for _, k := range []string{"include", "context", "output"} {
		if _, has := props[k]; has {
			t.Errorf("file_search_bytes has %s", k)
		}
	}
	lineProps, ok := (&SearchFilesTool{}).Parameters()["properties"].(map[string]any)
	if !ok {
		t.Fatal("file_search_lines has no properties")
	}
	for _, k := range []string{"include", "context", "output"} {
		if _, has := lineProps[k]; !has {
			t.Errorf("file_search_lines lacks %s", k)
		}
	}
}
