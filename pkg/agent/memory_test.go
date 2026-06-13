package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMemoryStore_UsesWorkspaceMemory(t *testing.T) {
	ws := t.TempDir()
	ms := NewMemoryStore(ws)

	wantDir := filepath.Join(ws, "memory")
	if ms.Dir() != wantDir {
		t.Fatalf("Dir() = %q, want %q", ms.Dir(), wantDir)
	}
	if ms.memoryFile != filepath.Join(wantDir, "MEMORY.md") {
		t.Fatalf("memoryFile = %q, want %q", ms.memoryFile, filepath.Join(wantDir, "MEMORY.md"))
	}
	if _, err := os.Stat(wantDir); err != nil {
		t.Fatalf("expected memory dir to be created: %v", err)
	}

	if err := ms.WriteLongTerm("hello"); err != nil {
		t.Fatalf("WriteLongTerm: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(wantDir, "MEMORY.md"))
	if err != nil {
		t.Fatalf("read MEMORY.md: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("MEMORY.md content = %q, want %q", string(data), "hello")
	}

	got := ms.ReadLongTerm()
	if got != "hello" {
		t.Fatalf("ReadLongTerm = %q, want %q", got, "hello")
	}
}

func TestMemoryStore_DailyNote(t *testing.T) {
	ws := t.TempDir()
	ms := NewMemoryStore(ws)

	if err := ms.AppendToday("entry-1"); err != nil {
		t.Fatalf("AppendToday: %v", err)
	}

	today := time.Now().Format("20060102")
	monthDir := today[:6]
	expected := filepath.Join(ws, "memory", monthDir, today+".md")
	data, err := os.ReadFile(expected)
	if err != nil {
		t.Fatalf("expected daily note at %s: %v", expected, err)
	}
	if !strings.Contains(string(data), "entry-1") {
		t.Fatalf("daily note missing entry: %q", string(data))
	}
}
