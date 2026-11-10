// ClawEh
// License: MIT

package files

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/tools"
)

// touch sets a file's modification time to base plus n seconds.
func touch(t *testing.T, p string, base time.Time, n int) {
	t.Helper()
	at := base.Add(time.Duration(n) * time.Second)
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
}

// resultPaths returns the paths a file_glob result lists, in order.
func resultPaths(out string) []string {
	_, body, _ := strings.Cut(out, "\n\n")
	if body == "" {
		return nil
	}
	return strings.Split(body, "\n")
}

// file_glob matches the pattern against paths below the searched folder
// (** across folders), lists files only, newest first, and skips dot-folders
// and the folder marker.
func TestGlob_MatchesNewestFirst(t *testing.T) {
	ws := t.TempDir()
	base := time.Now().Add(-time.Hour)
	for i, rel := range []string{"files/a.go", "files/sub/b.go", "files/sub/deep/c.go", "files/readme.md", "files/.git/x.go", "files/.hidden.go"} {
		p := filepath.Join(ws, rel)
		mustWrite(t, p, "x")
		touch(t, p, base, i)
	}
	if err := os.MkdirAll(filepath.Join(ws, "files", "dir.go"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(ws, "files", global.FolderMarkerFile), "marker")
	tool := NewGlobTool(ws, true)
	ctx := context.Background()

	res := tool.Execute(ctx, map[string]any{"pattern": "**/*.go", "path": "files"})
	if res.IsError {
		t.Fatalf("glob: %s", res.ForLLM)
	}
	want := []string{"files/.hidden.go", "files/sub/deep/c.go", "files/sub/b.go", "files/a.go"}
	if got := resultPaths(res.ForLLM); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("paths = %v, want %v (newest first; .git skipped; folders not listed)", got, want)
	}

	// * stays within one folder.
	res = tool.Execute(ctx, map[string]any{"pattern": "*.go", "path": "files"})
	if got := resultPaths(res.ForLLM); strings.Join(got, ",") != "files/.hidden.go,files/a.go" {
		t.Errorf("*.go = %v", got)
	}

	// The marker is never listed.
	res = tool.Execute(ctx, map[string]any{"pattern": "**", "path": "files"})
	if strings.Contains(res.ForLLM, global.FolderMarkerFile+"\n") || strings.HasSuffix(res.ForLLM, global.FolderMarkerFile) {
		t.Errorf("marker listed: %s", res.ForLLM)
	}

	// No match is a plain sentence, not an error.
	res = tool.Execute(ctx, map[string]any{"pattern": "**/*.rs", "path": "files"})
	if res.IsError || !strings.Contains(res.ForLLM, "No files match") {
		t.Errorf("no match = %v %s", res.IsError, res.ForLLM)
	}
}

// Without a path the search starts at the workspace; a relative path is
// relative to the workspace.
func TestGlob_DefaultPathIsWorkspace(t *testing.T) {
	ws := t.TempDir()
	mustWrite(t, filepath.Join(ws, "files", "notes", "todo.txt"), "x")
	tool := NewGlobTool(ws, true)
	res := tool.Execute(context.Background(), map[string]any{"pattern": "**/todo.txt"})
	if res.IsError || !strings.Contains(res.ForLLM, "files/notes/todo.txt") {
		t.Fatalf("default path: %s", res.ForLLM)
	}
}

// More than 200 matches: the 200 newest are listed, with a note.
func TestGlob_Truncated(t *testing.T) {
	ws := t.TempDir()
	base := time.Now().Add(-time.Hour)
	for i := range maxGlobResults + 5 {
		p := filepath.Join(ws, "files", fmt.Sprintf("f%03d.txt", i))
		mustWrite(t, p, "x")
		touch(t, p, base, i)
	}
	res := NewGlobTool(ws, true).Execute(context.Background(), map[string]any{"pattern": "*.txt", "path": "files"})
	got := resultPaths(res.ForLLM)
	if len(got) != maxGlobResults {
		t.Fatalf("listed %d paths, want %d", len(got), maxGlobResults)
	}
	if got[0] != "files/f204.txt" || got[len(got)-1] != "files/f005.txt" {
		t.Errorf("first/last = %s/%s, want the 200 newest", got[0], got[len(got)-1])
	}
	if !strings.Contains(res.ForLLM, "205 file(s) match") || !strings.Contains(res.ForLLM, "Showing the 200 newest") {
		t.Errorf("no truncation note: %s", strings.SplitN(res.ForLLM, "\n\n", 2)[0])
	}
}

// Bad input is refused.
func TestGlob_Refusals(t *testing.T) {
	ws := t.TempDir()
	mustWrite(t, filepath.Join(ws, "files", "a.txt"), "x")
	tool := NewGlobTool(ws, true)
	ctx := context.Background()
	for name, args := range map[string]map[string]any{
		"no pattern":      {},
		"blank pattern":   {"pattern": "  "},
		"invalid pattern": {"pattern": "[a"},
		"file as path":    {"pattern": "*", "path": "files/a.txt"},
		"missing path":    {"pattern": "*", "path": "files/none"},
	} {
		if res := tool.Execute(ctx, args); !res.IsError {
			t.Errorf("%s: not refused: %s", name, res.ForLLM)
		}
	}
}

// file_glob reaches what the other reading tools reach: the read scope and
// the allowed folders, by absolute path; nothing outside them, not even
// through a link inside an allowed folder.
func TestGlob_PermissionRules(t *testing.T) {
	ws := t.TempDir()
	mustWrite(t, filepath.Join(ws, "files", "ok.md"), "x")
	mustWrite(t, filepath.Join(ws, "AGENTS.md"), "x")
	src := t.TempDir()
	mustWrite(t, filepath.Join(src, "pkg", "main.go"), "x")
	outside := t.TempDir()
	mustWrite(t, filepath.Join(outside, "secret.go"), "x")
	if err := os.Symlink(filepath.Join(outside, "secret.go"), filepath.Join(src, "link.go")); err != nil {
		t.Fatal(err)
	}
	allowFolders(t, ws, src)
	SetReadScopeSubdirs([]string{"files", "skills"})
	defer SetReadScopeSubdirs(nil)
	tool := NewGlobTool(ws, true)
	ctx := context.Background()

	res := tool.Execute(ctx, map[string]any{"pattern": "**/*.go", "path": src})
	if res.IsError {
		t.Fatalf("glob in allowed folder: %s", res.ForLLM)
	}
	if got := resultPaths(res.ForLLM); len(got) != 1 || got[0] != filepath.Join(src, "pkg", "main.go") {
		t.Errorf("allowed folder paths = %v, want only pkg/main.go (the link out is not a match)", got)
	}

	// Outside the read scope and every allowed folder: refused.
	for _, p := range []string{".", outside} {
		res := tool.Execute(ctx, map[string]any{"pattern": "**", "path": p})
		if !res.IsError || !tools.IsExpectedRefusal(res.Err) {
			t.Errorf("glob in %s: want a refusal, got %v %s", p, res.IsError, res.ForLLM)
		}
	}
	if res := tool.Execute(ctx, map[string]any{"pattern": "**", "path": "files"}); res.IsError || !strings.Contains(res.ForLLM, "files/ok.md") {
		t.Errorf("glob in files/: %s", res.ForLLM)
	}
}

// file_glob is not a default tool.
func TestGlob_NotDefault(t *testing.T) {
	for _, d := range GlobalProvider.RegisterTools(global.Deps{}) {
		if d.Name == "glob" {
			if d.DefaultAllow == nil || *d.DefaultAllow {
				t.Error("file_glob must not be in the default tools")
			}
			if !strings.HasPrefix(d.Description, "Find files by name pattern") {
				t.Errorf("description = %q", d.Description)
			}
			return
		}
	}
	t.Fatal("glob is not registered")
}

// An absolute path inside the workspace works like the relative one and the
// paths come back as given; files with the same modification time are
// listed by path.
func TestGlob_AbsoluteWorkspacePathAndTies(t *testing.T) {
	ws := t.TempDir()
	at := time.Now().Add(-time.Hour)
	for _, rel := range []string{"files/b.go", "files/a.go", "files/c/a.go"} {
		p := filepath.Join(ws, rel)
		mustWrite(t, p, "x")
		touch(t, p, at, 0)
	}
	defaultScope(t)
	res := NewGlobTool(ws, true).Execute(context.Background(), map[string]any{"pattern": "**/*.go", "path": filepath.Join(ws, "files")})
	if res.IsError {
		t.Fatalf("absolute files/: %s", res.ForLLM)
	}
	want := []string{filepath.Join(ws, "files", "a.go"), filepath.Join(ws, "files", "b.go"), filepath.Join(ws, "files", "c", "a.go")}
	if got := resultPaths(res.ForLLM); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("paths = %v, want %v (same time: by path)", got, want)
	}
}

// A read-only allowed folder is searched like any other; a link in files/
// that leads out of the workspace is not a match, and naming the linked
// folder is refused.
func TestGlob_ReadOnlyFolderAndWorkspaceLink(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs file permissions that bind the test account")
	}
	ws := t.TempDir()
	mustWrite(t, filepath.Join(ws, "files", "in.go"), "x")
	outside := t.TempDir()
	mustWrite(t, filepath.Join(outside, "secret.go"), "x")
	for name, target := range map[string]string{"link.go": filepath.Join(outside, "secret.go"), "out": outside} {
		if err := os.Symlink(target, filepath.Join(ws, "files", name)); err != nil {
			t.Fatal(err)
		}
	}
	ro := t.TempDir()
	mustWrite(t, filepath.Join(ro, "pkg", "lib.go"), "x")
	if err := os.Chmod(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(ro, 0o700); err != nil {
			t.Error(err)
		}
	})
	allowFolders(t, ws, ro)
	defaultScope(t)
	tool := NewGlobTool(ws, true)
	ctx := context.Background()

	res := tool.Execute(ctx, map[string]any{"pattern": "**/*.go", "path": ro})
	if got := resultPaths(res.ForLLM); res.IsError || len(got) != 1 || got[0] != filepath.Join(ro, "pkg", "lib.go") {
		t.Errorf("read-only folder: %v %s", res.IsError, res.ForLLM)
	}
	res = tool.Execute(ctx, map[string]any{"pattern": "**/*.go", "path": "files"})
	if got := resultPaths(res.ForLLM); res.IsError || len(got) != 1 || got[0] != "files/in.go" {
		t.Errorf("files/ with links out = %v (%s), want only files/in.go", got, res.ForLLM)
	}
	if res := tool.Execute(ctx, map[string]any{"pattern": "**", "path": "files/out"}); !res.IsError || strings.Contains(res.ForLLM, "secret.go") {
		t.Errorf("glob in a linked folder outside: %v %s", res.IsError, res.ForLLM)
	}
}
