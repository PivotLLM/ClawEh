// ClawEh
// License: MIT

package utils

import (
	"archive/zip"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Extracted skill archives land under CLAW_HOME, so every directory and file
// ExtractZipFile creates is owner-only regardless of the modes in the archive.
func TestExtractZipFile_OwnerOnlyModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file permission bits are not enforced on Windows")
	}
	tmp := t.TempDir()
	zipPath := filepath.Join(tmp, "skill.zip")
	zf, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(zf)
	dirHdr := &zip.FileHeader{Name: "sub/"}
	dirHdr.SetMode(os.ModeDir | 0o755)
	if _, err = zw.CreateHeader(dirHdr); err != nil {
		t.Fatal(err)
	}
	fileHdr := &zip.FileHeader{Name: "sub/SKILL.md", Method: zip.Deflate}
	fileHdr.SetMode(0o644)
	w, err := zw.CreateHeader(fileHdr)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.Write([]byte("# skill\n")); err != nil {
		t.Fatal(err)
	}
	if err = zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err = zf.Close(); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(tmp, "out", "skill")
	if err = ExtractZipFile(zipPath, target); err != nil {
		t.Fatalf("ExtractZipFile: %v", err)
	}

	for _, dir := range []string{target, filepath.Join(target, "sub")} {
		fi, statErr := os.Stat(dir)
		if statErr != nil {
			t.Fatalf("stat %s: %v", dir, statErr)
		}
		if got := fi.Mode().Perm(); got != 0o700 {
			t.Errorf("%s mode = %04o, want 0700", dir, got)
		}
	}
	fi, err := os.Stat(filepath.Join(target, "sub", "SKILL.md"))
	if err != nil {
		t.Fatalf("stat SKILL.md: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("SKILL.md mode = %04o, want 0600", got)
	}
}
