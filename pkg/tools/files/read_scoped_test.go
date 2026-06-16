package files

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReadScoped confirms that when a read allowlist is configured, agent reads
// are confined to those workspace subdirs (files/, skills/) while config files at
// the workspace root are denied.
func TestReadScoped(t *testing.T) {
	workspace := t.TempDir()
	mustWrite(t, filepath.Join(workspace, "files", "note.txt"), "working file")
	mustWrite(t, filepath.Join(workspace, "skills", "demo", "SKILL.md"), "a skill")
	mustWrite(t, filepath.Join(workspace, "COGMEM.md"), "subsystem prompt — not for the agent")

	SetReadScopeSubdirs([]string{"files", "skills"})
	defer SetReadScopeSubdirs(nil)

	read := NewReadFileTool(workspace, true, MaxReadFileSize)

	// Inside the allowlist: allowed.
	if r := read.Execute(context.Background(), map[string]any{"path": "files/note.txt"}); r.IsError {
		t.Fatalf("read files/note.txt denied: %s", r.ForLLM)
	}
	if r := read.Execute(context.Background(), map[string]any{"path": "skills/demo/SKILL.md"}); r.IsError {
		t.Fatalf("read skills/demo/SKILL.md denied: %s", r.ForLLM)
	}

	// Outside the allowlist: denied.
	r := read.Execute(context.Background(), map[string]any{"path": "COGMEM.md"})
	if !r.IsError || !strings.Contains(r.ForLLM, "read denied") {
		t.Fatalf("expected COGMEM.md read to be denied, got isErr=%v %s", r.IsError, r.ForLLM)
	}

	// Listing the working area is allowed; listing the root is not.
	list := NewListDirTool(workspace, true)
	if r := list.Execute(context.Background(), map[string]any{"path": "files"}); r.IsError {
		t.Fatalf("list files/ denied: %s", r.ForLLM)
	}
	if r := list.Execute(context.Background(), map[string]any{"path": "."}); !r.IsError {
		t.Fatalf("expected listing the workspace root to be denied")
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
