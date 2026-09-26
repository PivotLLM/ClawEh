// ClawEh
// License: MIT

package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// DefaultConfig creates <CLAW_HOME>/agents/default on startup; the whole chain
// it creates is owner-only.
func TestDefaultConfig_CreatesOwnerOnlyWorkspace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file permission bits are not enforced on Windows")
	}
	home := filepath.Join(t.TempDir(), "claw-home")
	t.Setenv("CLAW_HOME", home)

	cfg := DefaultConfig()
	want := filepath.Join(home, "agents", "default")
	if cfg.WorkspacePath() != want {
		t.Fatalf("WorkspacePath = %q, want %q", cfg.WorkspacePath(), want)
	}
	for _, dir := range []string{home, filepath.Join(home, "agents"), want} {
		fi, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat %s: %v", dir, err)
		}
		if got := fi.Mode().Perm(); got != 0o700 {
			t.Errorf("%s mode = %04o, want 0700", dir, got)
		}
	}
}
