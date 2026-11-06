//go:build !windows

// ClawEh
// License: MIT

package agents

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/internal/perms"
)

// Task files and the tasks folder take exactly the agents area's configured
// modes, even under a umask that would narrow them.
func TestTaskFiles_ConfiguredModes(t *testing.T) {
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
	dir := tasksDirFor(ws)
	if err := writeStatus(dir, &TaskRecord{UUID: "t1"}); err != nil {
		t.Fatal(err)
	}
	if err := markRun(dir, "t1"); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{
		dir: 0o750, statusPath(dir, "t1"): 0o640, runPath(dir, "t1"): 0o640,
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
