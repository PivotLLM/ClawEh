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

// The default alerts log is created with the logs file mode (0600 unless
// security.permissions.logs says otherwise), whatever the umask, not only
// tightened at the next start.
func TestNewAlerter_LogMode(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, kv := range os.Environ() {
		if k, _, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(k, "ALERTER_") {
			t.Setenv(k, "")
		}
	}
	t.Setenv(alerter.EnvLogFile, "")
	for _, mode := range []os.FileMode{0o600, 0o640} {
		base := t.TempDir()
		if err := os.MkdirAll(filepath.Join(base, "logs"), 0o700); err != nil {
			t.Fatal(err)
		}
		old := syscall.Umask(0o022)
		a, path := newAlerter(base, mode)
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
		if got := info.Mode().Perm(); got != mode {
			t.Errorf("alerts log mode = %04o, want %04o", got, mode)
		}
	}
}
