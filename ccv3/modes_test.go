//go:build !windows

package ccv3

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// With WithModes, the library folder and every card file take exactly those
// modes, even under a umask that would narrow them.
func TestStore_WithModes(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	dir := filepath.Join(t.TempDir(), "cards")
	s, err := Open(dir, WithModes(0o750, 0o640))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Create("alice", []byte(`{"name":"Alice"}`)); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{dir: 0o750, filepath.Join(dir, "alice.json"): 0o640} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("%s = %04o, want %04o", path, got, want)
		}
	}
}
