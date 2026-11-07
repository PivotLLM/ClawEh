//go:build !windows

package gateway

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/tenebris-tech/alerter"

	"github.com/PivotLLM/ClawEh/config"
)

// The default alerts log, and a logs folder the alerter creates for it, take
// exactly the logs area's modes (security.permissions.logs), even under a
// umask that would narrow them.
func TestNewAlerter_LogMode(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, kv := range os.Environ() {
		if k, _, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(k, "ALERTER_") {
			t.Setenv(k, "")
		}
	}
	t.Setenv(alerter.EnvLogFile, "")
	for _, modes := range []config.Modes{{Dir: 0o700, File: 0o600}, {Dir: 0o750, File: 0o640}} {
		base := t.TempDir()
		old := syscall.Umask(0o077)
		a, path := newAlerter(base, modes)
		syscall.Umask(old)
		t.Cleanup(func() {
			if err := a.Close(context.Background()); err != nil {
				t.Errorf("Close: %v", err)
			}
		})

		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat alerts log: %v", err)
		}
		if got := info.Mode().Perm(); got != modes.File {
			t.Errorf("alerts log mode = %04o, want %04o", got, modes.File)
		}
		info, err = os.Stat(filepath.Dir(path))
		if err != nil {
			t.Fatalf("stat logs folder: %v", err)
		}
		if got := info.Mode().Perm(); got != modes.Dir {
			t.Errorf("logs folder mode = %04o, want %04o", got, modes.Dir)
		}
	}
}

// An alerts log left at another mode by an earlier run is set to exactly the
// logs file mode at start, widened as well as narrowed.
func TestNewAlerter_SetsExistingLogMode(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, kv := range os.Environ() {
		if k, _, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(k, "ALERTER_") {
			t.Setenv(k, "")
		}
	}
	t.Setenv(alerter.EnvLogFile, "")
	for _, c := range []struct{ before, want os.FileMode }{{0o600, 0o640}, {0o644, 0o600}} {
		base := t.TempDir()
		path := filepath.Join(base, "logs", alertsFileName)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, c.before); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, c.before); err != nil {
			t.Fatal(err)
		}
		a, _ := newAlerter(base, config.Modes{Dir: 0o700, File: c.want})
		t.Cleanup(func() {
			if err := a.Close(context.Background()); err != nil {
				t.Errorf("Close: %v", err)
			}
		})
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != c.want {
			t.Errorf("alerts log %04o → %04o, want %04o", c.before, got, c.want)
		}
	}
}
