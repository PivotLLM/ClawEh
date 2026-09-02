// ClawEh
// License: MIT

package files

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
)

func TestMounts_ReadWriteDeleteWithinMount(t *testing.T) {
	ws := t.TempDir()
	mountDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(mountDir, "stuff.md"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	SetMountsForWorkspace(ws, []MountSpec{{Name: "notes", Path: mountDir, Writable: true}})
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

// A mount is read-only by default: reads succeed, writes and deletes are rejected.
func TestMounts_ReadOnlyByDefault(t *testing.T) {
	ws := t.TempDir()
	mountDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(mountDir, "stuff.md"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	SetMountsForWorkspace(ws, []MountSpec{{Name: "notes", Path: mountDir}}) // Writable defaults false
	defer SetMountsForWorkspace(ws, nil)
	ctx := context.Background()

	// Read still works.
	read := NewReadFileTool(ws, true, MaxReadFileSize)
	if res := read.Execute(ctx, map[string]any{"path": "notes/stuff.md"}); res.IsError || !contains(res.ForLLM, "hello") {
		t.Fatalf("read of a read-only mount should work: %s", res.ForLLM)
	}

	// Write is rejected.
	write := NewWriteFileToolScoped(ws, true, "files")
	res := write.Execute(ctx, map[string]any{"path": "notes/new.md", "content": "world"})
	if !res.IsError || !contains(res.ForLLM, "read-only") {
		t.Fatalf("write to a read-only mount must be rejected: %s", res.ForLLM)
	}
	if _, err := os.Stat(filepath.Join(mountDir, "new.md")); !os.IsNotExist(err) {
		t.Fatalf("read-only mount must not have been written")
	}

	// Delete is rejected.
	del := NewDeleteFileToolScoped(ws, true, "files")
	if res := del.Execute(ctx, map[string]any{"path": "notes/stuff.md", "sure": true}); !res.IsError || !contains(res.ForLLM, "read-only") {
		t.Fatalf("delete in a read-only mount must be rejected: %s", res.ForLLM)
	}
	if _, err := os.Stat(filepath.Join(mountDir, "stuff.md")); err != nil {
		t.Fatalf("read-only mount file must still exist")
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

func TestMounts_HidesClawMarkerFromList(t *testing.T) {
	ws := t.TempDir()
	mountDir := t.TempDir()
	os.WriteFile(filepath.Join(mountDir, "a.md"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(mountDir, ".claw"), []byte(""), 0o600) // watermark marker
	SetMountsForWorkspace(ws, []MountSpec{{Name: "notes", Path: mountDir}})
	defer SetMountsForWorkspace(ws, nil)

	res := NewListDirTool(ws, true).Execute(context.Background(), map[string]any{"path": "notes"})
	if res.IsError {
		t.Fatalf("list failed: %s", res.ForLLM)
	}
	if !contains(res.ForLLM, "a.md") {
		t.Fatalf("expected a.md in listing: %s", res.ForLLM)
	}
	if contains(res.ForLLM, ".claw") {
		t.Fatalf(".claw marker must be hidden from listings: %s", res.ForLLM)
	}
}

// When Maestro is enabled, resolveAgentMounts auto-creates <workspace>/maestro
// and exposes it as a writable maestro/ mount so file_* can shuttle content.
func TestResolveAgentMounts_AutoMaestro(t *testing.T) {
	ws := t.TempDir()
	_ = os.MkdirAll(filepath.Join(ws, "files"), 0o755)

	agent := &config.AgentConfig{ID: "alice", Maestro: true}
	specs := resolveAgentMounts(agent, ws)
	if len(specs) != 1 || specs[0].Name != "maestro" || !specs[0].Writable {
		t.Fatalf("expected writable maestro mount, got %+v", specs)
	}
	if _, err := os.Stat(filepath.Join(ws, "maestro")); err != nil {
		t.Fatalf("maestro dir should be created: %v", err)
	}

	// Round-trip: write a report under maestro/, move it into files/.
	SetMountsForWorkspace(ws, specs)
	defer SetMountsForWorkspace(ws, nil)
	SetReadScopeSubdirs([]string{"files", "skills", "tasks", "tmp"})
	defer SetReadScopeSubdirs(nil)

	reportDir := filepath.Join(ws, "maestro", "projects", "demo", "reports")
	if err := os.MkdirAll(reportDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(reportDir, "out.md"), []byte("report"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	mv := NewMoveFileToolScoped(ws, true, "files")
	if res := mv.Execute(ctx, map[string]any{
		"source_path":      "maestro/projects/demo/reports/out.md",
		"destination_path": "files/out.md",
	}); res.IsError {
		t.Fatalf("move maestro → files: %s", res.ForLLM)
	}
	if b, err := os.ReadFile(filepath.Join(ws, "files", "out.md")); err != nil || string(b) != "report" {
		t.Fatalf("files/out.md = %q err=%v", b, err)
	}
	if _, err := os.Stat(filepath.Join(reportDir, "out.md")); !os.IsNotExist(err) {
		t.Fatalf("source should be gone after move")
	}

	// Reverse: files → maestro.
	if err := os.WriteFile(filepath.Join(ws, "files", "in.md"), []byte("input"), 0o644); err != nil {
		t.Fatal(err)
	}
	if res := mv.Execute(ctx, map[string]any{
		"source_path":      "files/in.md",
		"destination_path": "maestro/projects/demo/files/imported/in.md",
	}); res.IsError {
		t.Fatalf("move files → maestro: %s", res.ForLLM)
	}
	if b, err := os.ReadFile(filepath.Join(ws, "maestro", "projects", "demo", "files", "imported", "in.md")); err != nil || string(b) != "input" {
		t.Fatalf("maestro import path = %q err=%v", b, err)
	}

	// Maestro off → no auto mount.
	if got := resolveAgentMounts(&config.AgentConfig{ID: "bob"}, ws); len(got) != 0 {
		t.Fatalf("maestro off: expected nil, got %+v", got)
	}
}
