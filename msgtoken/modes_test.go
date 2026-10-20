//go:build !windows

package msgtoken

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// WithDirMode sets the store directory's mode (the agents area's), while the
// token file itself stays 0600.
func TestNewManager_WithDirMode(t *testing.T) {
	old := syscall.Umask(0o022)
	defer syscall.Umask(old)
	dir := filepath.Join(t.TempDir(), "state")
	storePath := filepath.Join(dir, "message-tokens.json")
	m, err := NewManager("alice", storePath, 60, 2, WithDirMode(0o750))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(m.Stop)
	for path, want := range map[string]os.FileMode{dir: 0o750, storePath: 0o600} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("%s mode = %04o, want %04o", filepath.Base(path), got, want)
		}
	}
}
