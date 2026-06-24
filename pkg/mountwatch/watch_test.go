// ClawEh
// License: MIT

package mountwatch

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectNewFiles_OnlyNewPathsFire(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "old.md")
	if err := os.WriteFile(old, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// First scan with no marker → baseline: nothing fires, .claw created.
	if got := detectNewFiles("notes", dir); len(got) != 0 {
		t.Fatalf("baseline should report no new files, got %v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, markerFile)); err != nil {
		t.Fatalf(".claw marker should be created on baseline: %v", err)
	}

	// Append to the existing file (new mtime, same path) → must NOT fire.
	if err := os.WriteFile(old, []byte("x-appended"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := detectNewFiles("notes", dir); len(got) != 0 {
		t.Fatalf("editing an existing file must not fire, got %v", got)
	}

	// A genuinely new file (new path) → fires once.
	newFile := filepath.Join(dir, "sub", "new.md")
	if err := os.MkdirAll(filepath.Dir(newFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newFile, []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := detectNewFiles("notes", dir)
	if len(got) != 1 || got[0] != "notes/sub/new.md" {
		t.Fatalf("want [notes/sub/new.md], got %v", got)
	}

	// Recorded set advanced → a second scan reports nothing, and editing the
	// now-known new file still does not fire.
	if got := detectNewFiles("notes", dir); len(got) != 0 {
		t.Fatalf("after advancing, expected nothing, got %v", got)
	}
	os.WriteFile(newFile, []byte("y2"), 0o644)
	if got := detectNewFiles("notes", dir); len(got) != 0 {
		t.Fatalf("editing a now-known file must not fire, got %v", got)
	}
}

func TestDetectNewFiles_IgnoresMarkerAndHidden(t *testing.T) {
	dir := t.TempDir()
	detectNewFiles("notes", dir) // baseline, creates .claw

	// A hidden file and the marker itself must never be reported.
	os.WriteFile(filepath.Join(dir, ".secret"), []byte("h"), 0o644)
	if got := detectNewFiles("notes", dir); len(got) != 0 {
		t.Fatalf("hidden files and the marker must be ignored, got %v", got)
	}
}
