// ClawEh - Cognitive Memory
// License: MIT

package cogmemhost

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestPruneSnapshots removes only pre-migration snapshots past the age limit;
// a young snapshot, the live database and its WAL sidecar stay.
func TestPruneSnapshots(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	write := func(name string, age time.Duration) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
		return path
	}
	old := write("cogmem.db.pre-v6.db", 31*24*time.Hour)
	older := write("cogmem.db.pre-v5.db", 400*24*time.Hour)
	young := write("cogmem.db.pre-v7.db", 2*24*time.Hour)
	live := write("cogmem.db", 400*24*time.Hour)
	wal := write("cogmem.db-wal", 400*24*time.Hour)

	removed, err := PruneSnapshots(dir, now, SnapshotMaxAge)
	if err != nil {
		t.Fatalf("PruneSnapshots: %v", err)
	}
	if len(removed) != 2 {
		t.Fatalf("removed %v, want the two old snapshots", removed)
	}
	for _, p := range []string{old, older} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s still present", filepath.Base(p))
		}
	}
	for _, p := range []string{young, live, wal} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s removed: %v", filepath.Base(p), err)
		}
	}
}

// TestPruneSnapshots_MissingDir: an agent without memory has no cogmem
// directory, which is not an error.
func TestPruneSnapshots_MissingDir(t *testing.T) {
	removed, err := PruneSnapshots(filepath.Join(t.TempDir(), "none"), time.Now(), SnapshotMaxAge)
	if err != nil || removed != nil {
		t.Fatalf("got %v, %v; want nil, nil", removed, err)
	}
}
