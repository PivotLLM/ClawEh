package gateway

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPruneOldLogs(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 6, 16, 12, 0, 0, 0, time.Local)

	mk := func(name string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	mk("20260101-claw.log")     // old → delete (cutoff = 2026-05-17)
	mk("20260101-error.log")    // old → delete
	mk("20260610-claw.log")     // within retention → keep
	mk("20260610-error.log")    // within retention → keep
	mk("claw.log")              // active → keep
	mk("error.log")             // active → keep
	mk("20260101-claw.log.gz")  // non-.log suffix → keep
	mk("notalog.txt")           // unrelated → keep

	pruneOldLogs(dir, 30, now)

	gone := func(n string) {
		if _, err := os.Stat(filepath.Join(dir, n)); !os.IsNotExist(err) {
			t.Fatalf("%s should have been deleted", n)
		}
	}
	kept := func(n string) {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			t.Fatalf("%s should have been kept: %v", n, err)
		}
	}
	gone("20260101-claw.log")
	gone("20260101-error.log")
	kept("20260610-claw.log")
	kept("20260610-error.log")
	kept("claw.log")
	kept("error.log")
	kept("20260101-claw.log.gz")
	kept("notalog.txt")
}

func TestPruneOldLogs_ZeroKeepsForever(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 6, 16, 12, 0, 0, 0, time.Local)
	old := "20200101-claw.log"
	if err := os.WriteFile(filepath.Join(dir, old), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	pruneOldLogs(dir, 0, now)
	if _, err := os.Stat(filepath.Join(dir, old)); err != nil {
		t.Fatalf("retention 0 must keep everything, but %s was removed: %v", old, err)
	}
}

func TestNextMidnight(t *testing.T) {
	now := time.Date(2026, 6, 16, 23, 59, 30, 0, time.Local)
	got := nextMidnight(now)
	want := time.Date(2026, 6, 17, 0, 0, 0, 0, time.Local)
	if !got.Equal(want) {
		t.Fatalf("nextMidnight = %v, want %v", got, want)
	}
}
