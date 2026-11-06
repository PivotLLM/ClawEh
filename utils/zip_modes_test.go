//go:build !windows

package utils

import (
	"archive/zip"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// An extracted skill takes exactly the modes given (its area's), whatever
// the archive says and whatever the umask.
func TestExtractZipFile_Modes(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	zipPath := filepath.Join(t.TempDir(), "skill.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	h := &zip.FileHeader{Name: "scripts/run.sh", Method: zip.Deflate}
	h.SetMode(0o755)
	w, err := zw.CreateHeader(h)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.Write([]byte("echo hi\n")); err != nil {
		t.Fatal(err)
	}
	if err = zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "skills", "demo")
	if err = ExtractZipFile(zipPath, target, 0o750, 0o640); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{
		target: 0o750, filepath.Join(target, "scripts"): 0o750, filepath.Join(target, "scripts", "run.sh"): 0o640,
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
