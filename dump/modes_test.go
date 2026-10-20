//go:build !windows

package dump

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// With security.permissions.logs set, the dumps directory and both files of a
// pair are created with the configured modes.
func TestWrite_ConfiguredModes(t *testing.T) {
	old := syscall.Umask(0o022)
	defer syscall.Umask(old)
	dir := filepath.Join(t.TempDir(), "dumps")
	base, err := Write(Dir{Path: dir, DirMode: 0o750, FileMode: 0o640}, "test", nil,
		json.RawMessage(`{}`), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	for path, want := range map[string]os.FileMode{
		dir:                              0o750,
		filepath.Join(dir, base+".json"): 0o640,
		filepath.Join(dir, base+".txt"):  0o640,
	} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("%s mode = %04o, want %04o", filepath.Base(path), got, want)
		}
	}
}
