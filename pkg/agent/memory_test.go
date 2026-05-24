package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMemoryStore_EmptyOverrideUsesWorkspaceMemory(t *testing.T) {
	ws := t.TempDir()
	ms := NewMemoryStore(ws, "")

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
}

func TestMemoryStore_NonEmptyOverrideRelocatesMemory(t *testing.T) {
	ws := t.TempDir()
	mem := t.TempDir()
	ms := NewMemoryStore(ws, mem)

	if ms.Dir() != mem {
		t.Fatalf("Dir() = %q, want %q", ms.Dir(), mem)
	}

	if err := ms.WriteLongTerm("relocated"); err != nil {
		t.Fatalf("WriteLongTerm: %v", err)
	}
	if _, err := os.Stat(filepath.Join(mem, "MEMORY.md")); err != nil {
		t.Fatalf("expected MEMORY.md under override: %v", err)
	}
	// And nothing should have been created under <workspace>/memory.
	if _, err := os.Stat(filepath.Join(ws, "memory", "MEMORY.md")); !os.IsNotExist(err) {
		t.Fatalf("expected NO MEMORY.md under workspace, got: %v", err)
	}

	got := ms.ReadLongTerm()
	if got != "relocated" {
		t.Fatalf("ReadLongTerm = %q, want %q", got, "relocated")
	}
}

func TestMemoryStore_DailyNoteUnderOverride(t *testing.T) {
	ws := t.TempDir()
	mem := t.TempDir()
	ms := NewMemoryStore(ws, mem)

	if err := ms.AppendToday("entry-1"); err != nil {
		t.Fatalf("AppendToday: %v", err)
	}

	today := time.Now().Format("20060102")
	monthDir := today[:6]
	expected := filepath.Join(mem, monthDir, today+".md")
	data, err := os.ReadFile(expected)
	if err != nil {
		t.Fatalf("expected daily note at %s: %v", expected, err)
	}
	if !strings.Contains(string(data), "entry-1") {
		t.Fatalf("daily note missing entry: %q", string(data))
	}
}

func TestMemoryStore_TildeExpansion(t *testing.T) {
	// Force a known HOME for deterministic expansion.
	home := t.TempDir()
	t.Setenv("HOME", home)

	ws := t.TempDir()
	ms := NewMemoryStore(ws, "~/custom-memory")

	want := filepath.Join(home, "custom-memory")
	if ms.Dir() != want {
		t.Fatalf("Dir() = %q, want %q (HOME=%s)", ms.Dir(), want, home)
	}
}
