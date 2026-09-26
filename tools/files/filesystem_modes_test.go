// ClawEh
// License: MIT

package files

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Parent directories created on the way to a written file are owner-only for
// both the unrestricted host backend and the sandboxed workspace backend.
func TestWriteFile_ParentDirsOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file permission bits are not enforced on Windows")
	}
	wantDir := func(path string) {
		t.Helper()
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if got := fi.Mode().Perm(); got != 0o700 {
			t.Errorf("%s mode = %04o, want 0700", path, got)
		}
	}

	t.Run("hostFs WriteFileExclMode", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "a", "b")
		fs := &hostFs{}
		if err := fs.WriteFileExclMode(filepath.Join(dir, "f.txt"), []byte("x"), 0o600); err != nil {
			t.Fatalf("WriteFileExclMode: %v", err)
		}
		wantDir(dir)
	})

	t.Run("sandboxFs WriteFileMode", func(t *testing.T) {
		workspace := t.TempDir()
		dir := filepath.Join(workspace, "a", "b")
		fs := &sandboxFs{workspace: workspace}
		if err := fs.WriteFileMode(filepath.Join(dir, "f.txt"), []byte("x"), 0o600); err != nil {
			t.Fatalf("WriteFileMode: %v", err)
		}
		wantDir(dir)
	})

	t.Run("sandboxFs WriteFileExclMode", func(t *testing.T) {
		workspace := t.TempDir()
		dir := filepath.Join(workspace, "a", "b")
		fs := &sandboxFs{workspace: workspace}
		if err := fs.WriteFileExclMode(filepath.Join(dir, "f.txt"), []byte("x"), 0o600); err != nil {
			t.Fatalf("WriteFileExclMode: %v", err)
		}
		wantDir(dir)
	})
}
