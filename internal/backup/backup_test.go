package backup

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRunCopiesPresentFilesSkipsMissing(t *testing.T) {
	root := t.TempDir()
	cfgSrc := filepath.Join(root, "config.json")
	if err := os.WriteFile(cfgSrc, []byte(`{"x":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(root, "backup")
	now := time.Date(2026, 6, 19, 3, 0, 0, 0, time.UTC)

	day, copied, err := Run(dest, now, map[string]string{
		cfgSrc:                           "config.json",
		filepath.Join(root, "jobs.json"): "jobs.json", // missing → skipped
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if copied != 1 {
		t.Fatalf("copied = %d, want 1 (missing jobs.json skipped)", copied)
	}
	want := filepath.Join(day, "config.json."+now.Format("20060102-150405"))
	got, err := os.ReadFile(want)
	if err != nil || string(got) != `{"x":1}` {
		t.Fatalf("backed-up config %s = %q err=%v", want, got, err)
	}
	if filepath.Base(day) != "20260619" {
		t.Fatalf("day folder = %s, want 20260619", filepath.Base(day))
	}
}

func TestPruneRemovesOldKeepsRecent(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 6, 19, 3, 0, 0, 0, time.UTC)
	// Create day-folders: today, 5 days ago (keep), 40 days ago (prune), plus a
	// non-date folder (ignored).
	for _, name := range []string{"20260619", "20260614", "20260510", "notes"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := Prune(root, 30, now)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if _, err := os.Stat(filepath.Join(root, "20260510")); !os.IsNotExist(err) {
		t.Error("40-day-old folder should be pruned")
	}
	for _, keep := range []string{"20260619", "20260614", "notes"} {
		if _, err := os.Stat(filepath.Join(root, keep)); err != nil {
			t.Errorf("%s should be kept: %v", keep, err)
		}
	}
	// retainDays <= 0 disables pruning.
	if n, _ := Prune(root, 0, now); n != 0 {
		t.Errorf("retainDays=0 should prune nothing, removed %d", n)
	}
}
