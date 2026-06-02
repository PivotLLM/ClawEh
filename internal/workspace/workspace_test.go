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
	Populate(ws, "")

	for _, f := range []string{"AGENTS.md", "BOOTSTRAP.md", "IDENTITY.md", "SOUL.md", "USER.md"} {
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
	Populate(ws, "") // fresh: seeds AGENTS.md + BOOTSTRAP.md

	if err := os.Remove(filepath.Join(ws, "BOOTSTRAP.md")); err != nil {
		t.Fatalf("remove BOOTSTRAP.md: %v", err)
	}

	Populate(ws, "") // restart: workspace already initialized (AGENTS.md present)

	if exists(t, filepath.Join(ws, "BOOTSTRAP.md")) {
		t.Error("BOOTSTRAP.md must not be recreated on an initialized workspace")
	}
	if !exists(t, filepath.Join(ws, "AGENTS.md")) {
		t.Error("AGENTS.md should still be present")
	}
}

// TestPopulate_RelocatedMemory_NoDefaultDir verifies that with memory relocated,
// the default <workspace>/memory is never created and the seed lands in the
// relocated directory.
func TestPopulate_RelocatedMemory_NoDefaultDir(t *testing.T) {
	ws := t.TempDir()
	memDir := filepath.Join(t.TempDir(), "relocated-mem")

	Populate(ws, memDir)

	if exists(t, filepath.Join(ws, "memory")) {
		t.Error("<workspace>/memory must not be created when memory is relocated")
	}
	if !exists(t, filepath.Join(memDir, "MEMORY.md")) {
		t.Error("starter memory should be seeded into the relocated directory")
	}
}

// TestPopulate_DeletedDefaultMemoryNotRecreated verifies that once a workspace is
// initialized, a deleted <workspace>/memory is not recreated on restart — the
// reported bug for relocated memory.
func TestPopulate_DeletedDefaultMemoryNotRecreated(t *testing.T) {
	ws := t.TempDir()
	memDir := filepath.Join(t.TempDir(), "relocated-mem")

	// First run initializes the workspace (writes AGENTS.md). Memory is relocated,
	// so <workspace>/memory is never created.
	Populate(ws, memDir)
	if !exists(t, filepath.Join(ws, "AGENTS.md")) {
		t.Fatal("expected workspace to be initialized")
	}

	// Simulate the user having (at some point) a default memory dir and deleting
	// it. A restart must not bring it back.
	Populate(ws, memDir)
	if exists(t, filepath.Join(ws, "memory")) {
		t.Error("<workspace>/memory must not reappear on an initialized, relocated-memory workspace")
	}
}
