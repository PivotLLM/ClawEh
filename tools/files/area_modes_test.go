//go:build !windows

// ClawEh
// License: MIT

package files

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/internal/perms"
)

// The file tools create folders and files with exactly the agents area's
// modes (security.permissions.agents), through both the sandboxed and the
// unrestricted backend, under a umask that would narrow them.
func TestFileToolsCreateWithAreaModes(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	home := t.TempDir()
	t.Setenv("CLAW_HOME", home)
	cfg := config.DefaultConfig()
	cfg.Security.Permissions.Agents = config.AreaPermissions{DirMode: "0750", FileMode: "0640"}
	perms.SetPolicy(perms.PolicyFor(cfg))
	t.Cleanup(func() { perms.SetPolicy(perms.Policy{}) })

	workspace := filepath.Join(home, "agents", "alice")
	if err := os.MkdirAll(workspace, 0o750); err != nil {
		t.Fatal(err)
	}
	for name, fsys := range map[string]fileSystem{
		"sandbox": &sandboxFs{workspace: workspace},
		"host":    &hostFs{},
	} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(workspace, name, "a")
			file := filepath.Join(dir, "notes.md")
			if err := fsys.WriteFile(file, []byte("x")); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			made := filepath.Join(workspace, name, "made", "deep")
			if err := fsys.MkdirAll(made); err != nil {
				t.Fatalf("MkdirAll: %v", err)
			}
			for path, want := range map[string]os.FileMode{
				filepath.Join(workspace, name): 0o750, dir: 0o750, file: 0o640,
				filepath.Join(workspace, name, "made"): 0o750, made: 0o750,
			} {
				fi, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				if got := fi.Mode().Perm(); got != want {
					t.Errorf("%s = %04o, want %04o", path, got, want)
				}
			}
		})
	}
}
