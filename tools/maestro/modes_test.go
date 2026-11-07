//go:build !windows

package maestro

import (
	"io/fs"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
)

// Everything Maestro creates in an agent's maestro folder, through the tools
// and through the runner, takes the agents area's configured modes. The umask
// is 0 so the test shows the modes ClawEh passes.
func TestMaestro_AgentsAreaModes(t *testing.T) {
	old := syscall.Umask(0)
	defer syscall.Umask(old)
	reg := newRegistration(t, "alice-modes")
	reg.cfg.Agents.List[0].Workspace = reg.ws
	reg.cfg.Security.Permissions.Agents = config.AreaPermissions{DirMode: "0750", FileMode: "0640"}

	h := reg.register(t, &scriptedRunner{})
	prepareProject(h, "audit")
	r := cachedRunnerFor(reg.agentID())
	startRun(t, r, "audit", nil)
	waitIdle(t, r)

	base := filepath.Join(reg.ws, "maestro")
	var files int
	err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		want := fs.FileMode(0o640)
		if d.IsDir() {
			want = 0o750
		} else {
			files++
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s = %04o, want %04o", path, got, want)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if files == 0 {
		t.Fatal("Maestro created no files")
	}
}

// TestRunnerCache_ModeChangeRebuildsIdleRunner: a changed agents area mode on
// an idle runner builds a new runner, which creates with the new modes.
func TestRunnerCache_ModeChangeRebuildsIdleRunner(t *testing.T) {
	reg := newRegistration(t, "alice-reload-modes")
	reg.cfg.Agents.List[0].Workspace = reg.ws
	reg.register(t, newGatedRunner())
	before := cachedRunnerFor(reg.agentID())

	reg.cfg.Security.Permissions.Agents = config.AreaPermissions{DirMode: "0750", FileMode: "0640"}
	reg.register(t, newGatedRunner())
	after := cachedEntry(reg.agentID())
	if after == nil || after.runner == before {
		t.Fatal("an idle runner was kept although the agents area's modes changed")
	}
	if want := (config.Modes{Dir: 0o750, File: 0o640}); after.spec.modes != want {
		t.Errorf("new runner's modes = %v, want %v", after.spec.modes, want)
	}
}
