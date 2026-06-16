package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMemoryStore_UsesWorkspaceMemory(t *testing.T) {
	ws := t.TempDir()
	ms := NewMemoryStore(ws)

	// MEMORY.md lives at the workspace root (a curated file).
	if ms.memoryFile != filepath.Join(ws, "MEMORY.md") {
		t.Fatalf("memoryFile = %q, want %q", ms.memoryFile, filepath.Join(ws, "MEMORY.md"))
	}

	if err := ms.WriteLongTerm("hello"); err != nil {
		t.Fatalf("WriteLongTerm: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(ws, "MEMORY.md"))
	if err != nil {
		t.Fatalf("read MEMORY.md: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("MEMORY.md content = %q, want %q", string(data), "hello")
	}

	if got := ms.ReadLongTerm(); got != "hello" {
		t.Fatalf("ReadLongTerm = %q, want %q", got, "hello")
	}
	if ctx := ms.GetMemoryContext(); !strings.Contains(ctx, "hello") {
		t.Fatalf("GetMemoryContext = %q, want it to contain the memory", ctx)
	}
}

// No memory/ directory should be created — daily notes were removed.
func TestMemoryStore_NoMemoryDir(t *testing.T) {
	ws := t.TempDir()
	_ = NewMemoryStore(ws)
	if _, err := os.Stat(filepath.Join(ws, "memory")); !os.IsNotExist(err) {
		t.Fatalf("memory/ directory should not be created (err=%v)", err)
	}
}
