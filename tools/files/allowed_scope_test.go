// ClawEh
// License: MIT

package files

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/PivotLLM/ClawEh/tools"
)

// An allowed folder that holds the whole workspace does not lift the
// workspace's rules: inside the workspace, by a relative or an absolute path,
// writes stay in files/, skills/ stays read-only and files/ itself cannot be
// removed; outside the workspace the folder is usable.
func TestAllowedFolder_ContainingWorkspaceKeepsWorkspaceRules(t *testing.T) {
	home := t.TempDir()
	ws := filepath.Join(home, ".claw", "workspace-alice")
	mustWrite(t, filepath.Join(ws, "files", "keep.md"), "keep")
	mustWrite(t, filepath.Join(ws, "skills", "s", "SKILL.md"), "skill")
	mustWrite(t, filepath.Join(ws, "secrets", "keys.md"), "secret")
	mustWrite(t, filepath.Join(home, "notes.md"), "home notes")
	allowFolders(t, ws, home)
	SetReadScopeSubdirs(effectiveReadSubdirs([]string{"files", "skills"}))
	t.Cleanup(func() { SetReadScopeSubdirs(nil) })
	ctx := context.Background()

	write := NewWriteFileToolScoped(ws, true, "files")
	read := NewReadFileTool(ws, true, MaxReadFileSize)
	for _, p := range []string{"AGENT.md", filepath.Join(ws, "AGENT.md"), "skills/s/SKILL.md", filepath.Join(ws, "skills", "s", "SKILL.md")} {
		res := write.Execute(ctx, map[string]any{"path": p, "content": "x", "overwrite": true})
		if !res.IsError || !tools.IsExpectedRefusal(res.Err) {
			t.Errorf("write %s should be refused by the workspace write scope, got: %s", p, res.ForLLM)
		}
	}
	if _, err := os.Stat(filepath.Join(ws, "AGENT.md")); !os.IsNotExist(err) {
		t.Fatal("AGENT.md was written")
	}
	mustRead(t, filepath.Join(ws, "skills", "s", "SKILL.md"), "skill")
	for _, p := range []string{"secrets/keys.md", filepath.Join(ws, "secrets", "keys.md")} {
		if res := read.Execute(ctx, map[string]any{"path": p}); !res.IsError {
			t.Errorf("read %s should be refused by the workspace read scope: %s", p, res.ForLLM)
		}
	}
	rmdir := NewRmdirToolScoped(ws, true, "files")
	for _, p := range []string{"files", filepath.Join(ws, "files")} {
		if res := rmdir.Execute(ctx, map[string]any{"path": p, "sure": true, "recursive": true}); !res.IsError {
			t.Errorf("rmdir %s should be refused: %s", p, res.ForLLM)
		}
	}
	mustRead(t, filepath.Join(ws, "files", "keep.md"), "keep")

	if res := write.Execute(ctx, map[string]any{"path": filepath.Join(ws, "files", "ok.md"), "content": "ok"}); res.IsError {
		t.Fatalf("absolute write into files/: %s", res.ForLLM)
	}
	if res := read.Execute(ctx, map[string]any{"path": filepath.Join(home, "notes.md")}); res.IsError || !contains(res.ForLLM, "home notes") {
		t.Fatalf("read outside the workspace in the folder: %s", res.ForLLM)
	}
	if res := write.Execute(ctx, map[string]any{"path": filepath.Join(home, "new.md"), "content": "y"}); res.IsError {
		t.Fatalf("write outside the workspace in the folder: %s", res.ForLLM)
	}
}

// A relative maestro/ path that a link leads out of every allowed area: with
// the agent unconfined it is served at its absolute place, never against the
// process's working directory.
func TestAllowedFolder_UnrestrictedEscapeIsAbsolute(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	mustWrite(t, filepath.Join(outside, "doc.md"), "outside doc")
	maestro := filepath.Join(ws, "maestro")
	if err := os.MkdirAll(maestro, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(maestro, "out")); err != nil {
		t.Fatal(err)
	}
	allowFolders(t, ws, maestro)
	// The process works elsewhere: a relative fallback would miss the file.
	t.Chdir(t.TempDir())

	read := NewReadFileTool(ws, false, MaxReadFileSize)
	res := read.Execute(context.Background(), map[string]any{"path": "maestro/out/doc.md"})
	if res.IsError || !contains(res.ForLLM, "outside doc") {
		t.Fatalf("unconfined read through a maestro/ link: %s", res.ForLLM)
	}
}

// A workspace configured through a symbolic link: a path given in its
// resolved form follows the same rules as one in its configured form, with
// or without allowed folders.
func TestWorkspaceResolvedFormSameAsConfigured(t *testing.T) {
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "ws")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(target, "files", "brief.md"), "brief")
	mustWrite(t, filepath.Join(target, "secrets", "keys.md"), "secret")
	SetReadScopeSubdirs(effectiveReadSubdirs([]string{"files", "skills"}))
	t.Cleanup(func() { SetReadScopeSubdirs(nil) })
	ctx := context.Background()

	for _, folders := range [][]string{nil, {t.TempDir()}} {
		allowFolders(t, link, folders...)
		read := NewReadFileTool(link, true, MaxReadFileSize)
		write := NewWriteFileToolScoped(link, true, "files")
		for _, root := range []string{link, target} {
			if res := read.Execute(ctx, map[string]any{"path": filepath.Join(root, "files", "brief.md")}); res.IsError || !contains(res.ForLLM, "brief") {
				t.Errorf("folders %v: read %s/files/brief.md: %s", folders, root, res.ForLLM)
			}
			if res := read.Execute(ctx, map[string]any{"path": filepath.Join(root, "secrets", "keys.md")}); !res.IsError {
				t.Errorf("folders %v: read %s/secrets/keys.md should be refused", folders, root)
			}
			if res := write.Execute(ctx, map[string]any{"path": filepath.Join(root, "files", "w.md"), "content": "w", "overwrite": true}); res.IsError {
				t.Errorf("folders %v: write %s/files/w.md: %s", folders, root, res.ForLLM)
			}
			if res := write.Execute(ctx, map[string]any{"path": filepath.Join(root, "x.md"), "content": "x", "overwrite": true}); !res.IsError {
				t.Errorf("folders %v: write %s/x.md should be refused", folders, root)
			}
		}
	}
}

// /srv/src2 is not inside /srv/src.
func TestAllowedFolder_PrefixIsNotParent(t *testing.T) {
	ws := t.TempDir()
	base := t.TempDir()
	src := filepath.Join(base, "src")
	src2 := filepath.Join(base, "src2")
	mustWrite(t, filepath.Join(src, "a.md"), "a")
	mustWrite(t, filepath.Join(src2, "b.md"), "b")
	allowFolders(t, ws, src)
	ctx := context.Background()

	if res := NewReadFileTool(ws, true, MaxReadFileSize).Execute(ctx, map[string]any{"path": filepath.Join(src2, "b.md")}); !res.IsError {
		t.Fatalf("a sibling sharing the folder's prefix should be refused: %s", res.ForLLM)
	}
	if res := NewWriteFileToolScoped(ws, true, "files").Execute(ctx, map[string]any{"path": filepath.Join(src2, "c.md"), "content": "c"}); !res.IsError {
		t.Fatal("writing a sibling sharing the folder's prefix should be refused")
	}
	if err := NewReader(readerConfig(), ws).Allowed(filepath.Join(src2, "b.md")); err == nil {
		t.Fatal("Reader allowed a sibling sharing the folder's prefix")
	}
}

// "<folder>/../other" leaves the folder and is refused for reads and writes.
func TestAllowedFolder_DotDotToSibling(t *testing.T) {
	ws := t.TempDir()
	base := t.TempDir()
	src := filepath.Join(base, "src")
	mustWrite(t, filepath.Join(src, "a.md"), "a")
	mustWrite(t, filepath.Join(base, "other", "b.md"), "b")
	allowFolders(t, ws, src)
	ctx := context.Background()
	p := src + "/../other/b.md"
	if res := NewReadFileTool(ws, true, MaxReadFileSize).Execute(ctx, map[string]any{"path": p}); !res.IsError {
		t.Fatalf("read %s should be refused: %s", p, res.ForLLM)
	}
	if res := NewWriteFileToolScoped(ws, true, "files").Execute(ctx, map[string]any{"path": src + "/../other/c.md", "content": "c"}); !res.IsError {
		t.Fatal("write through .. out of the folder should be refused")
	}
	if _, err := os.Stat(filepath.Join(base, "other", "c.md")); !os.IsNotExist(err) {
		t.Fatal("the write landed outside the folder")
	}
}

// Reader.Resolve and Allowed take an absolute path in an allowed folder, and
// refuse one outside it.
func TestReader_AbsoluteInAllowedFolder(t *testing.T) {
	ws := t.TempDir()
	src := t.TempDir()
	other := t.TempDir()
	mustWrite(t, filepath.Join(src, "sub", "doc.md"), "doc")
	mustWrite(t, filepath.Join(other, "doc.md"), "other")
	SetReadScopeSubdirs([]string{"files"})
	t.Cleanup(func() { SetReadScopeSubdirs(nil) })
	allowFolders(t, ws, src)
	r := NewReader(readerConfig(), ws)

	doc := filepath.Join(src, "sub", "doc.md")
	if got, err := r.Resolve(doc); err != nil || got != doc {
		t.Errorf("Resolve(%s) = %q, %v", doc, got, err)
	}
	if err := r.Allowed(doc); err != nil {
		t.Errorf("Allowed(%s) = %v", doc, err)
	}
	if _, err := r.Resolve(filepath.Join(other, "doc.md")); err == nil {
		t.Error("Resolve accepted a file outside every allowed folder")
	}
	if err := r.Allowed(filepath.Join(other, "doc.md")); err == nil {
		t.Error("Allowed accepted a file outside every allowed folder")
	}
}
