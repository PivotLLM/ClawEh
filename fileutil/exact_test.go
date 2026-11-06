//go:build !windows

// ClawEh
// License: MIT

package fileutil

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// MkdirAllExact gives every folder it creates exactly the mode, and
// WriteFileExact the file, under a umask that would narrow them; folders that
// exist keep their mode.
func TestExactModes(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	base := t.TempDir()
	if err := os.Chmod(base, 0o700); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "a", "b")
	if err := MkdirAllExact(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "f.txt")
	if err := WriteFileExact(file, []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]os.FileMode{
		base: 0o700, filepath.Join(base, "a"): 0o750, dir: 0o750, file: 0o640,
	} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("%s = %04o, want %04o", p, got, want)
		}
	}
}
