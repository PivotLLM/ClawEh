//go:build !windows

/******************************************************************************
 * Copyright (c) 2026 Tenebris Technologies Inc.                              *
 * Please see LICENSE file for details.                                       *
 ******************************************************************************/

package perms

import (
	"path/filepath"
	"syscall"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
)

// Policy.MkdirAll and Policy.WriteFile create each folder and file with
// exactly its area's mode under a narrowing umask, and ModesFor names it:
// the agents area for a workspace file, the secrets mode for a secret name,
// the default area elsewhere in the data dir.
func TestPolicyCreatesWithAreaModes(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	home := t.TempDir()
	cfg := operatorConfig(t, home)
	p := PolicyFor(cfg)

	file := filepath.Join(home, "agents", "alice", "tasks", "x.json")
	if err := p.WriteFile(file, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	cron := filepath.Join(home, "cron", "jobs.json")
	if err := p.WriteFile(cron, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]uint32{
		filepath.Join(home, "agents"):                   0o770,
		filepath.Join(home, "agents", "alice", "tasks"): 0o770,
		file:                        0o660,
		filepath.Join(home, "cron"): 0o710,
		cron:                        0o604,
	} {
		if got := modeOf(t, path); uint32(got) != want {
			t.Errorf("%s = %04o, want %04o", path, got, want)
		}
	}
	if got := p.ModesFor(filepath.Join(home, "agents", "alice", "API_TOKEN")).File; got != 0o600 {
		t.Errorf("secret-named file mode = %04o, want the secrets 0600", got)
	}
	if got := (Policy{}).ModesFor("/anywhere"); got != (config.Modes{Dir: 0o700, File: 0o600}) {
		t.Errorf("zero policy = %v, want 0700/0600", got)
	}
}
