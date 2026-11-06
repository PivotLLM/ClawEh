//go:build !windows

package workspace

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// A new workspace, its files/ folder and the seeded prompt files take exactly
// the configured agents modes, even under a umask that would narrow them.
func TestPopulate_ConfiguredModes(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	ws := filepath.Join(t.TempDir(), "alice")
	Populate(ws, 0o750, 0o640, nil, nil)
	for path, want := range map[string]os.FileMode{
		ws: 0o750, filepath.Join(ws, "files"): 0o750, filepath.Join(ws, "AGENTS.md"): 0o640,
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
