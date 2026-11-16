package upgrade

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"

	"github.com/PivotLLM/ClawEh/internal/install"
)

// TestUpgradeAndInstallAgreeOnBinaryName: `install` puts the binary at
// install.TargetBinaryPath, and `upgrade` takes the binary of that name out of
// a release archive to replace it. A different name on either side would make
// an upgrade fail, or leave the installed binary untouched.
func TestUpgradeAndInstallAgreeOnBinaryName(t *testing.T) {
	dir := t.TempDir()
	installed := filepath.Base(install.TargetBinaryPath(dir))

	archivePath := filepath.Join(dir, "release.tar.gz")
	f, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	content := []byte("binary")
	if err := tw.WriteHeader(&tar.Header{Name: installed, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	for _, c := range []interface{ Close() error }{tw, gz, f} {
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
	}

	dest := filepath.Join(dir, "out")
	if _, err := extractBinariesFromTarGz(archivePath, dest, filepath.Join(dir, "out-auth")); err != nil {
		t.Fatalf("upgrade does not take %q, the name install uses, from a release: %v", installed, err)
	}
	if got, err := os.ReadFile(dest); err != nil || string(got) != "binary" {
		t.Fatalf("extracted %q, %v", got, err)
	}
}
