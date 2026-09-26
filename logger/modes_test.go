package logger

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// Logs record prompts, tool arguments and errors, so the log directory, the
// active claw.log / error.log and the dated archives a roll produces are all
// owner-only.
func TestFileLogging_OwnerOnlyModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file permission bits are not enforced on Windows")
	}
	dir := filepath.Join(t.TempDir(), "logs")
	logPath := filepath.Join(dir, "claw.log")
	errPath := filepath.Join(dir, "error.log")

	if err := EnableFileLogging(logPath, false); err != nil {
		t.Fatalf("EnableFileLogging: %v", err)
	}
	defer DisableFileLogging()
	SetLevel(DEBUG)
	SetErrorLogLevel(WARN)
	WarnCF("test", "a warning line", nil)

	wantMode := func(path string, want os.FileMode) {
		t.Helper()
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("%s mode = %04o, want %04o", filepath.Base(path), got, want)
		}
	}
	wantMode(dir, 0o700)
	wantMode(logPath, 0o600)
	wantMode(errPath, 0o600)

	// Roll twice into the same day so the second roll takes the append path
	// (appendAndRemove) rather than a plain rename; both must keep 0600.
	ts := time.Date(2026, 1, 2, 10, 0, 0, 0, time.Local) //nolint:gosmopolitan // logs roll on the local calendar day by design
	for i := range 2 {
		for _, p := range []string{logPath, errPath} {
			if err := os.Chtimes(p, ts, ts); err != nil {
				t.Fatalf("chtimes %s: %v", p, err)
			}
		}
		if err := RollLogFile(); err != nil {
			t.Fatalf("RollLogFile #%d: %v", i+1, err)
		}
		WarnCF("test", "another warning line", nil)
	}
	wantMode(filepath.Join(dir, "20260102-claw.log"), 0o600)
	wantMode(filepath.Join(dir, "20260102-error.log"), 0o600)
	wantMode(logPath, 0o600)
	wantMode(errPath, 0o600)
}
