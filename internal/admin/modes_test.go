//go:build !windows

// ClawEh
// License: MIT

package admin

import (
	"errors"
	"os"
	"syscall"
	"testing"
)

// TestCredentialsFollowSecretsMode: with a secrets file mode that lets a
// group read, Write gives the file exactly that mode whatever the umask, Load
// accepts it and anything tighter, and refuses a bit beyond it with the chmod
// to the configured mode.
func TestCredentialsFollowSecretsMode(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	path := Path(t.TempDir())
	if err := Write(path, "alice", "a long enough password", 0o640); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o640 {
		t.Fatalf("written mode = %04o, want 0640", got)
	}
	if _, err = Load(path, 0o640); err != nil {
		t.Fatalf("Load at the configured mode: %v", err)
	}
	if err = os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = Load(path, 0o640); err != nil {
		t.Fatalf("Load of a tighter file: %v", err)
	}
	if err = os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = Load(path, 0o640)
	perr, ok := errors.AsType[*PermissionError](err)
	if !ok {
		t.Fatalf("Load of a world-readable file = %v, want *PermissionError", err)
	}
	if perr.Fix() != "chmod 640 "+path {
		t.Errorf("Fix() = %q", perr.Fix())
	}
}
