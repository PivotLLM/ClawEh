// ClawEh
// License: MIT

package fileutil

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWriteFileAtomic_WritesContentAndPerm(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	if err := WriteFileAtomic(path, []byte("hello"), 0o600); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("content = %q, want hello", got)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
			t.Errorf("perm = %v, want 0600", fi.Mode().Perm())
		}
	}
}

func TestWriteFileAtomic_CreatesParentDirs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "deep", "f.txt")
	if err := WriteFileAtomic(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFileAtomic with missing parents: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("file not created: %v", err)
	}
}

func TestWriteFileAtomic_OverwritesAtomically_NoTempLeft(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := WriteFileAtomic(path, []byte("old"), 0o644); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := WriteFileAtomic(path, []byte("new-content"), 0o644); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "new-content" {
		t.Errorf("content = %q, want new-content", got)
	}
	// No .tmp-* artifacts should remain on success.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if len(e.Name()) >= 5 && e.Name()[:5] == ".tmp-" {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
	if len(entries) != 1 {
		t.Errorf("expected exactly one file, got %d", len(entries))
	}
}

func TestWriteFileAtomic_LeavesOriginalOnDirError(t *testing.T) {
	// A path whose parent is an existing *file* (not a dir) makes MkdirAll fail,
	// so the call errors and writes nothing.
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(blocker, "child.txt") // parent is a file
	if err := WriteFileAtomic(bad, []byte("y"), 0o644); err == nil {
		t.Error("expected error when the parent path is a file")
	}
}
