package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func exists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	return err == nil
}

// TestPopulate_FreshWorkspace seeds all templates, including BOOTSTRAP.md and a
// starter memory, into a brand-new workspace.
func TestPopulate_FreshWorkspace(t *testing.T) {
	ws := t.TempDir()
	Populate(ws)

	for _, f := range []string{"AGENTS.md", "BOOTSTRAP.md", "COMPRESSION.md", "IDENTITY.md", "SOUL.md", "USER.md"} {
		if !exists(t, filepath.Join(ws, f)) {
			t.Errorf("expected %s to be seeded into a fresh workspace", f)
		}
	}
	if !exists(t, filepath.Join(ws, "memory", "MEMORY.md")) {
		t.Error("expected starter memory seeded into <workspace>/memory")
	}
}

// TestPopulate_BootstrapNotRecreated verifies a personalized agent that deleted
// BOOTSTRAP.md does not get it re-added on a subsequent startup.
func TestPopulate_BootstrapNotRecreated(t *testing.T) {
	ws := t.TempDir()
	Populate(ws) // fresh: seeds AGENTS.md + BOOTSTRAP.md

	if err := os.Remove(filepath.Join(ws, "BOOTSTRAP.md")); err != nil {
		t.Fatalf("remove BOOTSTRAP.md: %v", err)
	}

	Populate(ws) // restart: workspace already initialized (AGENTS.md present)

	if exists(t, filepath.Join(ws, "BOOTSTRAP.md")) {
		t.Error("BOOTSTRAP.md must not be recreated on an initialized workspace")
	}
	if !exists(t, filepath.Join(ws, "AGENTS.md")) {
		t.Error("AGENTS.md should still be present")
	}
}

// TestPopulate_CompressionNotRecreated verifies that an agent which deleted its
// optional COMPRESSION.md profile does not get it re-added on a later startup.
func TestPopulate_CompressionNotRecreated(t *testing.T) {
	ws := t.TempDir()
	Populate(ws) // fresh: seeds AGENTS.md + COMPRESSION.md

	if !exists(t, filepath.Join(ws, "COMPRESSION.md")) {
		t.Fatal("expected COMPRESSION.md seeded into a fresh workspace")
	}
	if err := os.Remove(filepath.Join(ws, "COMPRESSION.md")); err != nil {
		t.Fatalf("remove COMPRESSION.md: %v", err)
	}

	Populate(ws) // restart: workspace already initialized

	if exists(t, filepath.Join(ws, "COMPRESSION.md")) {
		t.Error("COMPRESSION.md must not be recreated on an initialized workspace")
	}
}

// TestPopulate_DeletedMemoryNotRecreated verifies that once a workspace is
// initialized, a deleted <workspace>/memory is not recreated on restart.
func TestPopulate_DeletedMemoryNotRecreated(t *testing.T) {
	ws := t.TempDir()

	// First run initializes the workspace (writes AGENTS.md) and seeds memory.
	Populate(ws)
	if !exists(t, filepath.Join(ws, "AGENTS.md")) {
		t.Fatal("expected workspace to be initialized")
	}
	if !exists(t, filepath.Join(ws, "memory", "MEMORY.md")) {
		t.Fatal("expected starter memory seeded on first run")
	}

	// Simulate the user deleting their memory directory. A restart must not
	// bring it back.
	if err := os.RemoveAll(filepath.Join(ws, "memory")); err != nil {
		t.Fatal(err)
	}
	Populate(ws)
	if exists(t, filepath.Join(ws, "memory")) {
		t.Error("<workspace>/memory must not reappear on an initialized workspace")
	}
}
