//go:build !windows

package common

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
)

// A file copied into the common folder, and the folders created for it, take
// exactly the destination area's modes, even under a umask that would narrow
// them.
func TestCopyFile_ConfiguredModes(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	src := filepath.Join(t.TempDir(), "notes.md")
	if err := os.WriteFile(src, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "common", "shared", "notes.md")
	if err := copyFile(src, dst, config.Modes{Dir: 0o770, File: 0o660}); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{filepath.Dir(dst): 0o770, dst: 0o660} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("%s = %04o, want %04o", path, got, want)
		}
	}
}
