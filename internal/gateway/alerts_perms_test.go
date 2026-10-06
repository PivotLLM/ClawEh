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
)

// The default alerts log is created 0600, whatever the umask, not only
// tightened at the next start.
func TestNewAlerter_LogIsPrivate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, kv := range os.Environ() {
		if k, _, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(k, "ALERTER_") {
			t.Setenv(k, "")
		}
	}
	t.Setenv(alerter.EnvLogFile, "")
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "logs"), 0o700); err != nil {
		t.Fatal(err)
	}
	old := syscall.Umask(0o022)
	a, path := newAlerter(base)
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
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("alerts log mode = %o, want 600", got)
	}
}
