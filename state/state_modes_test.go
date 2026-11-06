//go:build !windows

package state

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// The state folder and state.json take exactly the modes given (the agents
// area's), even under a umask that would narrow them.
func TestManager_ConfiguredModes(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	ws := t.TempDir()
	sm := NewManager(ws, 0o750, 0o640)
	if err := sm.SetPendingTurn("agent:alice:main", PendingTurn{}); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{
		filepath.Join(ws, "state"): 0o750, filepath.Join(ws, "state", "state.json"): 0o640,
	} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("%s = %04o, want %04o", path, got, want)
		}
	}
}
