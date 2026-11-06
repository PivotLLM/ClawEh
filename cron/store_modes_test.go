//go:build !windows

package cron

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/internal/perms"
)

// The cron store and its folder take exactly the default area's configured
// modes, even under a umask that would narrow them.
func TestCronStore_ConfiguredModes(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	home := t.TempDir()
	t.Setenv("CLAW_HOME", home)
	cfg := config.DefaultConfig()
	cfg.Security.Permissions.Default = config.AreaPermissions{DirMode: "0750", FileMode: "0640"}
	perms.SetPolicy(perms.PolicyFor(cfg))
	t.Cleanup(func() { perms.SetPolicy(perms.Policy{}) })

	path := filepath.Join(home, "cron", "jobs.json")
	cs := NewCronService(path, nil)
	cs.mu.Lock()
	err := cs.saveStoreUnsafe()
	cs.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]os.FileMode{filepath.Dir(path): 0o750, path: 0o640} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("%s = %04o, want %04o", p, got, want)
		}
	}
}
