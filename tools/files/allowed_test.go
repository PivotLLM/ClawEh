// ClawEh
// License: MIT

package files

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/tools"
)

// allowFolders installs folders for ws for the length of the test.
func allowFolders(t *testing.T, ws string, folders ...string) {
	t.Helper()
	SetAllowedFoldersForWorkspace(ws, folders)
	t.Cleanup(func() { SetAllowedFoldersForWorkspace(ws, nil) })
}

// Inside an allowed folder the file tools read, write and delete by the
// folder's absolute path, past the workspace write scope; outside every
// allowed folder an absolute path is still refused.
func TestAllowedFolder_ReadWriteDelete(t *testing.T) {
	ws := t.TempDir()
	src := t.TempDir()
	mustWrite(t, filepath.Join(src, "stuff.md"), "hello")
	allowFolders(t, ws, src)
	ctx := context.Background()

	read := NewReadFileTool(ws, true, MaxReadFileSize)
	if res := read.Execute(ctx, map[string]any{"path": filepath.Join(src, "stuff.md")}); res.IsError || !contains(res.ForLLM, "hello") {
		t.Fatalf("read in allowed folder: %s", res.ForLLM)
	}

	write := NewWriteFileToolScoped(ws, true, "files")
	if res := write.Execute(ctx, map[string]any{"path": filepath.Join(src, "sub", "new.md"), "content": "world"}); res.IsError {
		t.Fatalf("write in allowed folder: %s", res.ForLLM)
	}
	if b, err := os.ReadFile(filepath.Join(src, "sub", "new.md")); err != nil || string(b) != "world" {
		t.Fatalf("write did not land: %q %v", b, err)
	}

	del := NewDeleteFileToolScoped(ws, true, "files")
	if res := del.Execute(ctx, map[string]any{"path": filepath.Join(src, "stuff.md"), "sure": true}); res.IsError {
		t.Fatalf("delete in allowed folder: %s", res.ForLLM)
	}
	if _, err := os.Stat(filepath.Join(src, "stuff.md")); !os.IsNotExist(err) {
		t.Fatal("file should be gone")
	}

	// Outside every allowed folder: refused, as before.
	other := t.TempDir()
	mustWrite(t, filepath.Join(other, "secret.md"), "no")
	if res := read.Execute(ctx, map[string]any{"path": filepath.Join(other, "secret.md")}); !res.IsError {
		t.Fatalf("read outside allowed folders should be refused: %s", res.ForLLM)
	}
	if res := write.Execute(ctx, map[string]any{"path": filepath.Join(other, "x.md"), "content": "no"}); !res.IsError {
		t.Fatal("write outside allowed folders should be refused")
	}
	// The folder's name alone is no path: a relative path keeps resolving
	// from the workspace.
	if res := read.Execute(ctx, map[string]any{"path": filepath.Base(src) + "/sub/new.md"}); !res.IsError {
		t.Fatalf("relative path by folder name should not reach the folder: %s", res.ForLLM)
	}
}

// A ".." that leaves an allowed folder is placed where it lands, so it can
// reach nothing the agent could not reach by that path.
func TestAllowedFolder_ParentEscapeRefused(t *testing.T) {
	ws := t.TempDir()
	base := t.TempDir()
	src := filepath.Join(base, "src")
	mustWrite(t, filepath.Join(src, "a.md"), "a")
	mustWrite(t, filepath.Join(base, "secret.md"), "secret")
	allowFolders(t, ws, src)

	read := NewReadFileTool(ws, true, MaxReadFileSize)
	res := read.Execute(context.Background(), map[string]any{"path": src + "/../secret.md"})
	if !res.IsError {
		t.Fatalf("'..' out of the allowed folder must be refused, got: %s", res.ForLLM)
	}
}

// A link inside an allowed folder that leads outside every allowed area is
// refused for reads and writes; one that leads into the workspace is the
// workspace's, under its rules.
func TestAllowedFolder_SymlinkEscapeRefused(t *testing.T) {
	ws := t.TempDir()
	src := t.TempDir()
	outside := t.TempDir()
	mustWrite(t, filepath.Join(outside, "secret.md"), "secret")
	if err := os.Symlink(outside, filepath.Join(src, "out")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.md"), filepath.Join(src, "secret-link.md")); err != nil {
		t.Fatal(err)
	}
	allowFolders(t, ws, src)
	ctx := context.Background()

	read := NewReadFileTool(ws, true, MaxReadFileSize)
	write := NewWriteFileToolScoped(ws, true, "files")
	for _, p := range []string{filepath.Join(src, "out", "secret.md"), filepath.Join(src, "secret-link.md")} {
		res := read.Execute(ctx, map[string]any{"path": p})
		if !res.IsError || contains(res.ForLLM, "secret\n") {
			t.Errorf("read through %s should be refused: %s", p, res.ForLLM)
		}
		if !tools.IsExpectedRefusal(res.Err) {
			t.Errorf("read through %s: want a refusal, got %v", p, res.Err)
		}
	}
	if res := write.Execute(ctx, map[string]any{"path": filepath.Join(src, "out", "new.md"), "content": "x"}); !res.IsError {
		t.Fatal("write through a link out of the folder should be refused")
	}
	if _, err := os.Stat(filepath.Join(outside, "new.md")); !os.IsNotExist(err) {
		t.Fatal("the write landed outside the allowed folder")
	}
}

// A link between two allowed folders is followed: it leads to a folder the
// agent may use anyway.
func TestAllowedFolder_SymlinkBetweenFolders(t *testing.T) {
	ws := t.TempDir()
	src := t.TempDir()
	ref := t.TempDir()
	mustWrite(t, filepath.Join(ref, "doc.md"), "ref doc")
	if err := os.Symlink(ref, filepath.Join(src, "ref")); err != nil {
		t.Fatal(err)
	}
	allowFolders(t, ws, src, ref)

	read := NewReadFileTool(ws, true, MaxReadFileSize)
	if res := read.Execute(context.Background(), map[string]any{"path": filepath.Join(src, "ref", "doc.md")}); res.IsError || !contains(res.ForLLM, "ref doc") {
		t.Fatalf("link into another allowed folder: %s", res.ForLLM)
	}
}

// Writing into a folder the account cannot write is attempted and returns a
// short permission refusal naming the path; reading it still works.
func TestAllowedFolder_ReadOnlyByPermissions(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs file permissions that bind the test account")
	}
	ws := t.TempDir()
	ro := t.TempDir()
	mustWrite(t, filepath.Join(ro, "a.md"), "readable")
	if err := os.Chmod(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(ro, 0o700); err != nil {
			t.Error(err)
		}
	})
	allowFolders(t, ws, ro)
	ctx := context.Background()

	read := NewReadFileTool(ws, true, MaxReadFileSize)
	if res := read.Execute(ctx, map[string]any{"path": filepath.Join(ro, "a.md")}); res.IsError || !contains(res.ForLLM, "readable") {
		t.Fatalf("read in a read-only folder: %s", res.ForLLM)
	}

	target := filepath.Join(ro, "new.md")
	write := NewWriteFileToolScoped(ws, true, "files")
	res := write.Execute(ctx, map[string]any{"path": target, "content": "x"})
	if !res.IsError {
		t.Fatal("write into a read-only folder should fail")
	}
	want := "permission denied: " + target + " is read-only for this app"
	if !contains(res.ForLLM, want) {
		t.Errorf("error = %q, want it to contain %q", res.ForLLM, want)
	}
	if !tools.IsExpectedRefusal(res.Err) {
		t.Errorf("want an expected refusal, got %v", res.Err)
	}

	mkdir := NewMkdirToolScoped(ws, true, "files")
	if res := mkdir.Execute(ctx, map[string]any{"path": filepath.Join(ro, "sub")}); !res.IsError || !contains(res.ForLLM, "is read-only for this app") {
		t.Errorf("mkdir in a read-only folder: %s", res.ForLLM)
	}
}

// A folder writable below a read-only root takes writes: the temp file of an
// atomic write sits beside the target.
func TestAllowedFolder_WritableSubfolder(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs file permissions that bind the test account")
	}
	ws := t.TempDir()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "inbox"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(root, 0o700); err != nil {
			t.Error(err)
		}
	})
	allowFolders(t, ws, root)

	write := NewWriteFileToolScoped(ws, true, "files")
	if res := write.Execute(context.Background(), map[string]any{"path": filepath.Join(root, "inbox", "x.md"), "content": "x"}); res.IsError {
		t.Fatalf("write into a writable subfolder: %s", res.ForLLM)
	}
}

// An allowed folder itself cannot be removed or moved.
func TestAllowedFolder_RootNotRemovable(t *testing.T) {
	ws := t.TempDir()
	src := t.TempDir()
	allowFolders(t, ws, src)
	ctx := context.Background()
	if res := NewRmdirToolScoped(ws, true, "files").Execute(ctx, map[string]any{"path": src, "sure": true}); !res.IsError {
		t.Fatal("removing an allowed folder should be refused")
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("folder gone: %v", err)
	}
}

func TestAllowedFolder_ListAndHideMarker(t *testing.T) {
	ws := t.TempDir()
	src := t.TempDir()
	mustWrite(t, filepath.Join(src, "a.md"), "x")
	mustWrite(t, filepath.Join(src, ".claw"), "") // new-file marker
	allowFolders(t, ws, src)

	res := NewListDirTool(ws, true).Execute(context.Background(), map[string]any{"path": src})
	if res.IsError || !contains(res.ForLLM, "a.md") {
		t.Fatalf("list should show a.md: %s", res.ForLLM)
	}
	if contains(res.ForLLM, ".claw") {
		t.Fatalf(".claw marker must be hidden from listings: %s", res.ForLLM)
	}
}

// With Maestro on, maestro/ is part of the workspace the file tools read and
// write, by its relative path, with no mount and past the write scope.
func TestAgentFolders_Maestro(t *testing.T) {
	ws := t.TempDir()
	src := t.TempDir()
	agent := &config.AgentConfig{
		ID: "alice", Maestro: &config.MaestroConfig{Enabled: true},
		AllowedFolders: []config.AllowedFolderConfig{{Path: src}, {Path: "relative"}, {Path: filepath.Join(src, "missing")}, {Path: src + "/"}},
	}
	got := agentFolders(agent, ws)
	want := []string{src, filepath.Join(ws, "maestro")}
	if !slices.Equal(got, want) {
		t.Fatalf("agentFolders = %v, want %v", got, want)
	}
	if info, err := os.Stat(filepath.Join(ws, "maestro")); err != nil || !info.IsDir() {
		t.Fatalf("maestro dir not created: %v", err)
	}
	allowFolders(t, ws, got...)
	SetReadScopeSubdirs(effectiveReadSubdirs([]string{"files", "skills"}))
	t.Cleanup(func() { SetReadScopeSubdirs(nil) })
	ctx := context.Background()

	write := NewWriteFileToolScoped(ws, true, "files")
	if res := write.Execute(ctx, map[string]any{"path": "maestro/projects/p/notes.md", "content": "plan"}); res.IsError {
		t.Fatalf("write maestro/: %s", res.ForLLM)
	}
	read := NewReadFileTool(ws, true, MaxReadFileSize)
	if res := read.Execute(ctx, map[string]any{"path": "maestro/projects/p/notes.md"}); res.IsError || !contains(res.ForLLM, "plan") {
		t.Fatalf("read maestro/: %s", res.ForLLM)
	}
	abs := filepath.Join(ws, "maestro", "projects", "p", "notes.md")
	if res := read.Execute(ctx, map[string]any{"path": abs}); res.IsError || !contains(res.ForLLM, "plan") {
		t.Fatalf("read maestro by absolute path: %s", res.ForLLM)
	}

	// Maestro off: maestro/ is outside the read and write scopes again.
	agent.Maestro = &config.MaestroConfig{Enabled: false}
	if got := agentFolders(agent, ws); !slices.Equal(got, []string{src}) {
		t.Fatalf("maestro off: agentFolders = %v", got)
	}
	allowFolders(t, ws, agentFolders(agent, ws)...)
	write = NewWriteFileToolScoped(ws, true, "files")
	if res := write.Execute(ctx, map[string]any{"path": "maestro/x.md", "content": "x"}); !res.IsError {
		t.Fatal("maestro off: writing maestro/ should be refused")
	}
}

// A link under maestro/ that leads outside every allowed area is refused like
// any other.
func TestAgentFolders_MaestroSymlinkEscape(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	mustWrite(t, filepath.Join(outside, "secret.md"), "secret")
	agent := &config.AgentConfig{ID: "alice", Maestro: &config.MaestroConfig{Enabled: true}}
	allowFolders(t, ws, agentFolders(agent, ws)...)
	if err := os.Symlink(outside, filepath.Join(ws, "maestro", "out")); err != nil {
		t.Fatal(err)
	}
	read := NewReadFileTool(ws, true, MaxReadFileSize)
	res := read.Execute(context.Background(), map[string]any{"path": "maestro/out/secret.md"})
	if !res.IsError || strings.Contains(res.ForLLM, "secret\n") {
		t.Fatalf("link out of maestro/ should be refused: %s", res.ForLLM)
	}
}
