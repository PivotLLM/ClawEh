package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSweepReceivedMedia(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()

	old := filepath.Join(dir, "old.ogg")
	fresh := filepath.Join(dir, "fresh.ogg")
	for _, p := range []string{old, fresh} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Age "old" past the TTL; leave "fresh" recent.
	stale := now.Add(-receivedMediaTTL - time.Hour)
	if err := os.Chtimes(old, stale, stale); err != nil {
		t.Fatal(err)
	}
	// A subdirectory must be left alone even if old.
	sub := filepath.Join(dir, "keep")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(sub, stale, stale); err != nil {
		t.Fatal(err)
	}

	sweepReceivedMedia(dir, now)

	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("old file should have been swept, err=%v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("fresh file should remain: %v", err)
	}
	if _, err := os.Stat(sub); err != nil {
		t.Errorf("subdirectory should remain: %v", err)
	}
}
