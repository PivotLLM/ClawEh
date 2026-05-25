package tools

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// newRedirect builds a redirectFs with workspace at wsDir and memory root at
// memDir (a sibling, NOT under workspace). Returns the fs plus both abs roots.
func newRedirect(t *testing.T) (fileSystem, string, string) {
	t.Helper()
	wsDir := t.TempDir()
	memDir := t.TempDir()
	fs := buildFsWithMemoryRedirect(wsDir, true, nil, memDir)
	return fs, wsDir, memDir
}

func TestRedirectFs_WriteAndReadHappyPath(t *testing.T) {
	fs, wsDir, memDir := newRedirect(t)

	// Write to "memory/MEMORY.md" — must land in memDir, NOT in wsDir/memory.
	if err := fs.WriteFile("memory/MEMORY.md", []byte("notes")); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(memDir, "MEMORY.md")); err != nil {
		t.Fatalf("expected MEMORY.md under memDir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wsDir, "memory", "MEMORY.md")); !os.IsNotExist(err) {
		t.Fatalf("expected NO MEMORY.md under workspace, got: %v", err)
	}

	data, err := fs.ReadFile("memory/MEMORY.md")
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if string(data) != "notes" {
		t.Fatalf("unexpected content: %q", string(data))
	}
}

func TestRedirectFs_NestedSubdirectory(t *testing.T) {
	fs, _, memDir := newRedirect(t)

	if err := fs.WriteFile("memory/202612/20261203.md", []byte("daily")); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(memDir, "202612", "20261203.md")); err != nil {
		t.Fatalf("expected nested file under memDir: %v", err)
	}
}

func TestRedirectFs_ListMemoryDir(t *testing.T) {
	fs, _, memDir := newRedirect(t)
	if err := os.WriteFile(filepath.Join(memDir, "MEMORY.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(memDir, "202612"), 0o700); err != nil {
		t.Fatal(err)
	}

	entries, err := fs.ReadDir("memory")
	if err != nil {
		t.Fatalf("ReadDir(memory) failed: %v", err)
	}
	names := map[string]bool{}
	for _, e := range entries {
		names[e.Name()] = true
	}
	if !names["MEMORY.md"] || !names["202612"] {
		t.Fatalf("expected MEMORY.md and 202612 dir, got %v", names)
	}
}

func TestRedirectFs_NonMemoryPathGoesToBase(t *testing.T) {
	fs, wsDir, memDir := newRedirect(t)

	if err := fs.WriteFile("notes.txt", []byte("hello")); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wsDir, "notes.txt")); err != nil {
		t.Fatalf("expected notes.txt under wsDir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(memDir, "notes.txt")); !os.IsNotExist(err) {
		t.Fatalf("notes.txt must NOT land under memDir, got: %v", err)
	}
}

func TestRedirectFs_TraversalRejectedInMemorySubtree(t *testing.T) {
	fs, wsDir, memDir := newRedirect(t)

	// "memory/sub/../../escape.txt" cleans to "escape.txt". The redirect
	// routes it to base (since the cleaned path no longer starts with
	// "memory/"); the workspace sandbox then keeps it inside wsDir. Result:
	// the write SUCCEEDS but lands in wsDir, NEVER in memDir, and never
	// escapes either root.
	if err := fs.WriteFile("memory/sub/../../escape.txt", []byte("x")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(memDir, "escape.txt")); !os.IsNotExist(err) {
		t.Fatalf("escape.txt must NOT land under memDir, got: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wsDir, "escape.txt")); err != nil {
		t.Fatalf("expected escape.txt under wsDir (sandbox contained), got: %v", err)
	}

	// A path that actively tries to escape BOTH roots with extra "..":
	// "memory/../../escape.txt" cleans to "../escape.txt", which is not
	// workspace-local and must be rejected outright.
	if err := fs.WriteFile("memory/../../escape.txt", []byte("x")); err == nil {
		t.Fatal("expected non-local traversal write to be rejected")
	}
}

func TestRedirectFs_TraversalRejectedAtWorkspaceLevel(t *testing.T) {
	fs, _, _ := newRedirect(t)

	if err := fs.WriteFile("../escape.txt", []byte("x")); err == nil {
		t.Fatal("expected workspace-level traversal write to be rejected")
	}
}

func TestRedirectFs_SymlinkEscapeRejected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on Windows")
	}
	fs, _, memDir := newRedirect(t)

	// Create a symlink INSIDE memDir pointing to a directory OUTSIDE both roots.
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(memDir, "escape")); err != nil {
		t.Fatal(err)
	}

	// Reading through the symlink must be rejected by os.Root.
	if _, err := fs.ReadFile("memory/escape/secret.txt"); err == nil {
		t.Fatal("expected symlink escape read to be rejected")
	}
}

func TestRedirectFs_DefaultMemoryDirNoRedirect(t *testing.T) {
	wsDir := t.TempDir()
	// memoryRoot == <workspace>/memory → buildFsWithMemoryRedirect must return
	// the plain sandbox so behaviour matches the legacy code path byte-for-byte.
	fs := buildFsWithMemoryRedirect(wsDir, true, nil, filepath.Join(wsDir, "memory"))
	if _, ok := fs.(*redirectFs); ok {
		t.Fatal("expected plain sandboxFs for default memory dir, got redirectFs")
	}

	if err := fs.WriteFile("memory/MEMORY.md", []byte("x")); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wsDir, "memory", "MEMORY.md")); err != nil {
		t.Fatalf("expected MEMORY.md under wsDir/memory: %v", err)
	}
}

func TestRedirectFs_EmptyMemoryDirNoRedirect(t *testing.T) {
	wsDir := t.TempDir()
	fs := buildFsWithMemoryRedirect(wsDir, true, nil, "")
	if _, ok := fs.(*redirectFs); ok {
		t.Fatal("expected plain sandboxFs for empty memory dir, got redirectFs")
	}
}

func TestRedirectFs_AbsolutePathInsideWorkspaceRedirects(t *testing.T) {
	fs, wsDir, memDir := newRedirect(t)
	abs := filepath.Join(wsDir, "memory", "MEMORY.md")

	if err := fs.WriteFile(abs, []byte("via-abs")); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(memDir, "MEMORY.md"))
	if err != nil {
		t.Fatalf("expected MEMORY.md under memDir: %v", err)
	}
	if string(got) != "via-abs" {
		t.Fatalf("unexpected content: %q", string(got))
	}
}

func TestRedirectFs_OpenMemoryFile(t *testing.T) {
	fs, _, memDir := newRedirect(t)
	if err := os.WriteFile(filepath.Join(memDir, "MEMORY.md"), []byte("opened"), 0o600); err != nil {
		t.Fatal(err)
	}

	f, err := fs.Open("memory/MEMORY.md")
	if err != nil {
		t.Fatalf("open failed: %v", err)
	}
	defer f.Close()
	buf := make([]byte, 32)
	n, _ := f.Read(buf)
	if !strings.Contains(string(buf[:n]), "opened") {
		t.Fatalf("unexpected content: %q", string(buf[:n]))
	}
}
