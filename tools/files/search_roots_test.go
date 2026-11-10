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

// defaultScope sets the read scope a default config gives the file tools
// (files/, skills/ and the always-readable folders) for the test.
func defaultScope(t *testing.T) {
	t.Helper()
	SetReadScopeSubdirs(effectiveReadSubdirs([]string{"files", "skills"}))
	t.Cleanup(func() { SetReadScopeSubdirs(nil) })
}

// With no path, file_glob and file_search_lines search every workspace folder
// the agent may read (maestro/ included when it is a folder of the agent),
// with paths relative to the workspace; not the workspace's own files and not
// the allowed folders outside it. file_search_bytes keeps "." (refused here).
func TestNoPath_SearchesReadableWorkspaceFolders(t *testing.T) {
	ws := t.TempDir()
	for _, rel := range []string{"files/a.md", "skills/demo/SKILL.md", "maestro/p/plan.md", "tasks/t.md", "AGENTS.md"} {
		mustWrite(t, filepath.Join(ws, rel), "needle\n")
	}
	ext := t.TempDir()
	mustWrite(t, filepath.Join(ext, "outside.md"), "needle\n")
	allowFolders(t, ws, filepath.Join(ws, "maestro"), ext)
	defaultScope(t)
	ctx := context.Background()
	want := []string{"files/a.md", "skills/demo/SKILL.md", "maestro/p/plan.md", "tasks/t.md"}

	res := NewGlobTool(ws, true).Execute(ctx, map[string]any{"pattern": "**/*.md"})
	if res.IsError {
		t.Fatalf("glob without path: %s", res.ForLLM)
	}
	got := strings.Join(resultPaths(res.ForLLM), ",")
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("glob lacks %s: %s", w, got)
		}
	}
	if strings.Contains(got, "AGENTS.md") || strings.Contains(got, "outside.md") {
		t.Errorf("glob reached past the readable workspace folders: %s", got)
	}
	// The pattern is matched from the workspace.
	res = NewGlobTool(ws, true).Execute(ctx, map[string]any{"pattern": "files/*.md"})
	if p := resultPaths(res.ForLLM); len(p) != 1 || p[0] != "files/a.md" {
		t.Errorf("files/*.md = %v", p)
	}

	res = NewSearchLinesTool(ws, true).Execute(ctx, map[string]any{"query": "needle"})
	if res.IsError {
		t.Fatalf("search without path: %s", res.ForLLM)
	}
	for _, w := range want {
		if !strings.Contains(res.ForLLM, w+":1:") {
			t.Errorf("search lacks %s:\n%s", w, res.ForLLM)
		}
	}
	if strings.Contains(res.ForLLM, "AGENTS.md") || strings.Contains(res.ForLLM, "outside.md") {
		t.Errorf("search reached past the readable workspace folders:\n%s", res.ForLLM)
	}
	// include with "/" matches from the workspace too.
	res = NewSearchLinesTool(ws, true).Execute(ctx, map[string]any{"query": "needle", "include": "skills/**", "output": "files"})
	if !strings.HasSuffix(res.ForLLM, "\n\nskills/demo/SKILL.md") {
		t.Errorf("include skills/** without path:\n%s", res.ForLLM)
	}

	res = NewSearchBytesTool(ws, true).Execute(ctx, map[string]any{"query": "needle"})
	if !res.IsError || !strings.Contains(res.ForLLM, "read denied") {
		t.Errorf("file_search_bytes without path changed: %v %s", res.IsError, res.ForLLM)
	}
}

// repoTree builds a repository inside files/: files/repo with .git (a folder,
// or a file as in a git worktree), a root .gitignore and a nested one.
func repoTree(t *testing.T, gitFile bool) string {
	t.Helper()
	ws := searchTree(t, map[string]string{
		"repo/.gitignore":                   "*.log\n/build/\n",
		"repo/sub/.gitignore":               "gen_*\n",
		"repo/sub/deep/keep.txt":            "needle\n",
		"repo/sub/deep/x.log":               "needle\n",
		"repo/sub/deep/gen_a.txt":           "needle\n",
		"repo/sub/deep/build/out.txt":       "needle\n",
		"repo/build/out.txt":                "needle\n",
		"repo/build/inner/keep-in-build.md": "needle\n",
	})
	git := filepath.Join(ws, "files", "repo", ".git")
	if gitFile {
		mustWrite(t, git, "gitdir: /elsewhere\n")
	} else if err := os.Mkdir(git, 0o700); err != nil {
		t.Fatal(err)
	}
	return ws
}

// Searching a folder inside a repository applies the .gitignore files of its
// parents up to the repository root, anchored where git anchors them.
func TestSearchLines_ParentGitignores(t *testing.T) {
	for _, gitFile := range []bool{false, true} {
		ws := repoTree(t, gitFile)
		out := searchLines(t, ws, map[string]any{"query": "needle", "path": "files/repo/sub/deep"})
		if !strings.Contains(out, "files/repo/sub/deep/keep.txt:") {
			t.Errorf("gitFile=%v: keep.txt missing:\n%s", gitFile, out)
		}
		// "/build/" is anchored at the repository root: deep/build is kept.
		if !strings.Contains(out, "files/repo/sub/deep/build/out.txt:") {
			t.Errorf("gitFile=%v: an anchored parent pattern matched below its folder:\n%s", gitFile, out)
		}
		for _, bad := range []string{"x.log", "gen_a.txt"} {
			if strings.Contains(out, bad) {
				t.Errorf("gitFile=%v: %s searched although a parent .gitignore ignores it:\n%s", gitFile, bad, out)
			}
		}
		// file_glob is not affected by .gitignore.
		res := NewGlobTool(ws, true).Execute(context.Background(), map[string]any{"pattern": "**/*.log", "path": "files/repo/sub/deep"})
		if !strings.Contains(res.ForLLM, "x.log") {
			t.Errorf("file_glob applied .gitignore: %s", res.ForLLM)
		}
	}
}

// A folder the parent .gitignore files ignore is still searched when the
// agent names it; only the .gitignore files inside it apply.
func TestSearchLines_NamedIgnoredFolder(t *testing.T) {
	ws := repoTree(t, false)
	out := searchLines(t, ws, map[string]any{"query": "needle", "path": "files/repo/build"})
	if !strings.Contains(out, "files/repo/build/out.txt:") || !strings.Contains(out, "keep-in-build.md:") {
		t.Errorf("a named ignored folder was not searched:\n%s", out)
	}
}

// Without a .git above the searched folder, parent .gitignore files are not
// read.
func TestSearchLines_NoRepositoryNoParents(t *testing.T) {
	ws := searchTree(t, map[string]string{".gitignore": "*.log\n", "sub/x.log": "needle\n"})
	out := searchLines(t, ws, map[string]any{"query": "needle", "path": "files/sub"})
	if !strings.Contains(out, "files/sub/x.log:") {
		t.Errorf("a parent .gitignore was read without a repository:\n%s", out)
	}
}

// The walk up stops where the agent may not read: a repository whose root is
// the workspace itself (outside files/) is not found, and its .gitignore is
// not read.
func TestSearchLines_ParentsStopAtReadableArea(t *testing.T) {
	ws := searchTree(t, map[string]string{"sub/x.log": "needle\n"})
	mustWrite(t, filepath.Join(ws, ".gitignore"), "*.log\n")
	if err := os.Mkdir(filepath.Join(ws, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	defaultScope(t)
	out := searchLines(t, ws, map[string]any{"query": "needle", "path": "files/sub"})
	if !strings.Contains(out, "files/sub/x.log:") {
		t.Errorf("a .gitignore outside the readable area was applied:\n%s", out)
	}
}

// A parent .gitignore follows the same rules as one inside: a link is not
// read.
func TestSearchLines_ParentGitignoreLinkSkipped(t *testing.T) {
	ws := searchTree(t, map[string]string{"repo/sub/x.log": "needle\n"})
	if err := os.Mkdir(filepath.Join(ws, "files", "repo", ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(ws, "rules"), "*.log\n")
	if err := os.Symlink(filepath.Join(ws, "rules"), filepath.Join(ws, "files", "repo", ".gitignore")); err != nil {
		t.Fatal(err)
	}
	out := searchLines(t, ws, map[string]any{"query": "needle", "path": "files/repo/sub"})
	if !strings.Contains(out, "files/repo/sub/x.log:") {
		t.Errorf("a parent .gitignore link was followed:\n%s", out)
	}
}
