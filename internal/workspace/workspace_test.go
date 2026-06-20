package workspace

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/PivotLLM/ClawEh/templates"
)

func exists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	return err == nil
}

// TestPopulate_FreshWorkspace seeds all templates and a starter memory into a
// brand-new workspace.
func TestPopulate_FreshWorkspace(t *testing.T) {
	ws := t.TempDir()
	Populate(ws)

	for _, f := range []string{"AGENTS.md", "COMPRESSION.md", "IDENTITY.md", "SOUL.md", "USER.md"} {
		if !exists(t, filepath.Join(ws, f)) {
			t.Errorf("expected %s to be seeded into a fresh workspace", f)
		}
	}
	if !exists(t, filepath.Join(ws, "MEMORY.md")) {
		t.Error("expected MEMORY.md seeded at the workspace root")
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

// TestPopulate_CogmemRecreated verifies that the consolidation prompt
// (COGMEM.md) — unlike the seed-once files — IS recreated when deleted, with the
// current template content. This is the path operators use to refresh an agent
// onto an updated prompt: delete COGMEM.md, restart.
func TestPopulate_CogmemRecreated(t *testing.T) {
	ws := t.TempDir()
	Populate(ws) // fresh: seeds AGENTS.md + COGMEM.md

	cogmemPath := filepath.Join(ws, "COGMEM.md")
	if !exists(t, cogmemPath) {
		t.Fatal("expected COGMEM.md seeded into a fresh workspace")
	}
	if err := os.Remove(cogmemPath); err != nil {
		t.Fatalf("remove COGMEM.md: %v", err)
	}

	Populate(ws) // restart: workspace already initialized

	got, err := os.ReadFile(cogmemPath)
	if err != nil {
		t.Fatalf("COGMEM.md must be recreated on an initialized workspace: %v", err)
	}
	want, err := templates.FS.ReadFile("COGMEM.md")
	if err != nil {
		t.Fatalf("read embedded template: %v", err)
	}
	if string(got) != string(want) {
		t.Error("recreated COGMEM.md does not match the current embedded template")
	}
}

// TestPopulate_NoMemoryDir verifies Populate never creates a <workspace>/memory
// directory (daily notes were removed).
func TestPopulate_NoMemoryDir(t *testing.T) {
	ws := t.TempDir()
	Populate(ws)
	if exists(t, filepath.Join(ws, "memory")) {
		t.Error("<workspace>/memory must not be created")
	}
}
