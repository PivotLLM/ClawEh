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

// The seen-files marker takes exactly the file mode of the area it lies in
// (here an agent's workspace), even under a umask that would narrow it.
func TestWriteSeen_ConfiguredMode(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	home := t.TempDir()
	t.Setenv("CLAW_HOME", home)
	cfg := config.DefaultConfig()
	cfg.Security.Permissions.Agents = config.AreaPermissions{DirMode: "0750", FileMode: "0640"}
	perms.SetPolicy(perms.PolicyFor(cfg))
	t.Cleanup(func() { perms.SetPolicy(perms.Policy{}) })

	folder := filepath.Join(home, "agents", "alice", "inbox")
	if err := os.MkdirAll(folder, 0o750); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(folder, markerFile)
	w := New(func() *config.Config { return cfg }, nil, 0, nil)
	w.writeSeen("Alice", folder, marker, []string{"a.txt"})
	fi, err := os.Stat(marker)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o640 {
		t.Errorf("marker = %04o, want 0640", got)
	}
}
