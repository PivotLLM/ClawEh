// ClawEh
// License: MIT

package mountwatch

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDetectNewFiles_BaselineThenDetectThenAdvance(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "old.md")
	if err := os.WriteFile(old, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// First scan with no marker → baseline: nothing fires, .claw created.
	if got := detectNewFiles("notes", dir); len(got) != 0 {
		t.Fatalf("baseline should report no new files, got %v", got)
	}
	marker := filepath.Join(dir, markerFile)
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf(".claw marker should be created on baseline: %v", err)
	}

	// Add a file in a subdirectory.
	newFile := filepath.Join(dir, "sub", "new.md")
	if err := os.MkdirAll(filepath.Dir(newFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newFile, []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Pin mtimes (all in the past relative to "now" so the post-fire touch wins):
	// watermark = now-5s; old.md before it (won't fire); new.md after it (fires).
	now := time.Now()
	cht := func(p string, ago time.Duration) {
		ts := now.Add(-ago)
		if err := os.Chtimes(p, ts, ts); err != nil {
			t.Fatal(err)
		}
	}
	cht(marker, 5*time.Second)
	cht(old, 10*time.Second)
	cht(newFile, 2*time.Second)

	got := detectNewFiles("notes", dir)
	if len(got) != 1 || got[0] != "notes/sub/new.md" {
		t.Fatalf("want [notes/sub/new.md], got %v", got)
	}

	// Marker advanced past the new file → a second scan reports nothing.
	if got := detectNewFiles("notes", dir); len(got) != 0 {
		t.Fatalf("after advancing the watermark, expected no new files, got %v", got)
	}
}

func TestDetectNewFiles_IgnoresMarkerAndHidden(t *testing.T) {
	dir := t.TempDir()
	detectNewFiles("notes", dir) // baseline, creates .claw
	marker := filepath.Join(dir, markerFile)
	now := time.Now()
	os.Chtimes(marker, now.Add(-5*time.Second), now.Add(-5*time.Second))

	// A hidden file and the marker itself must never be reported even when newer.
	os.WriteFile(filepath.Join(dir, ".secret"), []byte("h"), 0o644)
	os.Chtimes(filepath.Join(dir, ".secret"), now, now)
	os.Chtimes(marker, now.Add(-5*time.Second), now.Add(-5*time.Second)) // keep watermark old

	if got := detectNewFiles("notes", dir); len(got) != 0 {
		t.Fatalf("hidden files and the marker must be ignored, got %v", got)
	}
}
