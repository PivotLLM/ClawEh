//go:build !windows

package folderwatch

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/internal/perms"
)

// The seen-files marker, kept in the agent's workspace, and the folders made
// for it take exactly the agents area's modes, even under a umask that would
// narrow them.
func TestWriteSeen_ConfiguredMode(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	home := t.TempDir()
	t.Setenv("CLAW_HOME", home)
	cfg := config.DefaultConfig()
	cfg.Security.Permissions.Agents = config.AreaPermissions{DirMode: "0750", FileMode: "0640"}
	perms.SetPolicy(perms.PolicyFor(cfg))
	t.Cleanup(func() { perms.SetPolicy(perms.Policy{}) })

	ws := filepath.Join(home, "agents", "alice")
	if err := os.MkdirAll(ws, 0o750); err != nil {
		t.Fatal(err)
	}
	folder := t.TempDir()
	marker := markerPath(ws, "alice", folder)
	w := New(func() *config.Config { return cfg }, nil, 0, nil, nil)
	w.writeSeen("alice", "Alice", folder, marker, []string{"a.txt"}, true)

	for _, dir := range []string{filepath.Join(ws, "state"), filepath.Join(ws, markerDir)} {
		fi, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != 0o750 {
			t.Errorf("%s = %04o, want 0750", dir, got)
		}
	}
	fi, err := os.Stat(marker)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o640 {
		t.Errorf("marker = %04o, want 0640", got)
	}
}
