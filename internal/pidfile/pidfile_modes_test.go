// ClawEh - gateway PID file
// License: MIT

package pidfile

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// The PID file and the data directory it may have to create are owner-only:
// nothing under CLAW_HOME is world-readable.
func TestWriteOwnerOnlyModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file permission bits are not enforced on Windows")
	}
	dir := filepath.Join(t.TempDir(), "data")
	if err := Write(dir); err != nil {
		t.Fatalf("Write: %v", err)
	}
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if got := di.Mode().Perm(); got != 0o700 {
		t.Errorf("data dir mode = %04o, want 0700", got)
	}
	fi, err := os.Stat(Path(dir))
	if err != nil {
		t.Fatalf("stat pid file: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("pid file mode = %04o, want 0600", got)
	}
}
