// ClawEh
// License: MIT

package files

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestMounts_ReadWriteDeleteWithinMount(t *testing.T) {
	ws := t.TempDir()
	mountDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(mountDir, "stuff.md"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	SetMountsForWorkspace(ws, []MountSpec{{Name: "notes", Path: mountDir}})
	defer SetMountsForWorkspace(ws, nil)
	ctx := context.Background()

	// Read a file inside the mount.
	read := NewReadFileTool(ws, true, MaxReadFileSize)
	if res := read.Execute(ctx, map[string]any{"path": "notes/stuff.md"}); res.IsError || !contains(res.ForLLM, "hello") {
		t.Fatalf("read notes/stuff.md: %s", res.ForLLM)
	}

	// Write into the mount — must succeed even though the write scope is files/.
	write := NewWriteFileToolScoped(ws, true, "files")
	if res := write.Execute(ctx, map[string]any{"path": "notes/new.md", "content": "world"}); res.IsError {
		t.Fatalf("write notes/new.md should be allowed: %s", res.ForLLM)
	}
	if b, _ := os.ReadFile(filepath.Join(mountDir, "new.md")); string(b) != "world" {
		t.Fatalf("mount write did not land on disk: %q", string(b))
	}

	// A non-mount, out-of-scope write is still denied.
	if res := write.Execute(ctx, map[string]any{"path": "elsewhere/x.md", "content": "no"}); !res.IsError {
		t.Fatalf("write outside files/ and mounts should be denied")
	}

	// Delete inside the mount.
	del := NewDeleteFileToolScoped(ws, true, "files")
	if res := del.Execute(ctx, map[string]any{"path": "notes/stuff.md", "sure": true}); res.IsError {
		t.Fatalf("delete notes/stuff.md: %s", res.ForLLM)
	}
	if _, err := os.Stat(filepath.Join(mountDir, "stuff.md")); !os.IsNotExist(err) {
		t.Fatalf("mount file should be gone")
	}
}

func TestMounts_RejectsParentEscape(t *testing.T) {
	ws := t.TempDir()
	mountDir := t.TempDir()
	SetMountsForWorkspace(ws, []MountSpec{{Name: "notes", Path: mountDir}})
	defer SetMountsForWorkspace(ws, nil)

	read := NewReadFileTool(ws, true, MaxReadFileSize)
	res := read.Execute(context.Background(), map[string]any{"path": "notes/../../../../etc/hostname"})
	if !res.IsError {
		t.Fatalf("'..' escape above the mount must be rejected, got: %s", res.ForLLM)
	}
}

func TestMounts_ListMountRoot(t *testing.T) {
	ws := t.TempDir()
	mountDir := t.TempDir()
	os.WriteFile(filepath.Join(mountDir, "a.md"), []byte("x"), 0o644)
	SetMountsForWorkspace(ws, []MountSpec{{Name: "notes", Path: mountDir}})
	defer SetMountsForWorkspace(ws, nil)

	list := NewListDirTool(ws, true)
	res := list.Execute(context.Background(), map[string]any{"path": "notes"})
	if res.IsError || !contains(res.ForLLM, "a.md") {
		t.Fatalf("list notes/ should show a.md: %s", res.ForLLM)
	}
}
