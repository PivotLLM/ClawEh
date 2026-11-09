// ClawEh
// License: MIT

package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAccessibleFolders(t *testing.T) {
	cb := &ContextBuilder{}
	if got := cb.accessibleFolders(); got != "files/ (read/write), skills/ (read-only)" {
		t.Fatalf("base folders = %q", got)
	}

	// Allowed folders are listed by their real path; one the account cannot
	// write is marked read-only; a missing one is left out. Maestro is part
	// of the workspace.
	base := t.TempDir()
	src := filepath.Join(base, "src")
	ref := filepath.Join(base, "ref")
	for _, d := range []string{src, ref} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	readOnly := os.Geteuid() != 0
	if readOnly {
		if err := os.Chmod(ref, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := os.Chmod(ref, 0o700); err != nil {
				t.Error(err)
			}
		})
	}
	cb.allowedFolders = []string{src, ref, filepath.Join(base, "missing")}
	cb.maestroEnabled = true
	want := "files/ (read/write), skills/ (read-only), maestro/ (read/write), " + src + ", " + ref
	if readOnly {
		want += " (read-only)"
	}
	if got := cb.accessibleFolders(); got != want {
		t.Fatalf("with folders = %q, want %q", got, want)
	}
}
