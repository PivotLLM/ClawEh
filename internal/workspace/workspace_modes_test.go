package workspace

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestPopulate_OwnerOnlyModes verifies a freshly created workspace, its files/
// subdir and the seeded templates are owner-only (0700/0600).
func TestPopulate_OwnerOnlyModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file permission bits are not enforced on Windows")
	}
	ws := filepath.Join(t.TempDir(), "agent")
	Populate(ws)

	for _, dir := range []string{ws, filepath.Join(ws, "files")} {
		fi, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat %s: %v", dir, err)
		}
		if got := fi.Mode().Perm(); got != 0o700 {
			t.Errorf("%s mode = %04o, want 0700", dir, got)
		}
	}
	fi, err := os.Stat(filepath.Join(ws, "AGENTS.md"))
	if err != nil {
		t.Fatalf("stat AGENTS.md: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("AGENTS.md mode = %04o, want 0600", got)
	}
}
