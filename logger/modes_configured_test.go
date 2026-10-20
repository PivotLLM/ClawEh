//go:build !windows

package logger

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// With security.permissions.logs set, the log directory, claw.log, error.log
// and the archives a roll produces are created with the configured modes.
func TestFileLogging_ConfiguredModes(t *testing.T) {
	old := syscall.Umask(0o022)
	defer syscall.Umask(old)
	SetFileModes(0o750, 0o640)
	defer SetFileModes(0o700, 0o600)

	dir := filepath.Join(t.TempDir(), "logs")
	logPath := filepath.Join(dir, "claw.log")
	errPath := filepath.Join(dir, "error.log")
	if err := EnableFileLogging(logPath, false); err != nil {
		t.Fatalf("EnableFileLogging: %v", err)
	}
	defer DisableFileLogging()
	SetErrorLogLevel(WARN)
	WarnCF("test", "a warning line", nil)

	ts := time.Date(2026, 1, 2, 10, 0, 0, 0, time.Local) //nolint:gosmopolitan // logs roll on the local calendar day by design
	for _, p := range []string{logPath, errPath} {
		if err := os.Chtimes(p, ts, ts); err != nil {
			t.Fatalf("chtimes %s: %v", p, err)
		}
	}
	if err := RollLogFile(); err != nil {
		t.Fatalf("RollLogFile: %v", err)
	}
	for path, want := range map[string]os.FileMode{
		dir:                                     0o750,
		logPath:                                 0o640,
		errPath:                                 0o640,
		filepath.Join(dir, "20260102-claw.log"): 0o640,
	} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("%s mode = %04o, want %04o", filepath.Base(path), got, want)
		}
	}
}
