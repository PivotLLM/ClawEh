// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package layout

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
)

// newConfig returns the default config with CLAW_HOME at a fresh temp dir.
func newConfig(t *testing.T) (*config.Config, string) {
	t.Helper()
	home := filepath.Join(t.TempDir(), "claw-home")
	t.Setenv("CLAW_HOME", home)
	return config.DefaultConfig(), home
}

func assertPrivateDir(t *testing.T, dir string) {
	t.Helper()
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat %s: %v", dir, err)
	}
	if !fi.IsDir() {
		t.Fatalf("%s is not a directory", dir)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o700 {
		t.Errorf("%s mode = %04o, want 0700", dir, fi.Mode().Perm())
	}
}

func TestPrepareFreshStartCreatesDirectories(t *testing.T) {
	cfg, home := newConfig(t)

	Prepare(cfg)

	assertPrivateDir(t, home)
	for _, dir := range []string{"internal", "cli", "skills", "common"} {
		assertPrivateDir(t, filepath.Join(home, dir))
	}
}

// A second start finds everything in place and changes nothing.
func TestPrepareIsIdempotent(t *testing.T) {
	cfg, home := newConfig(t)

	Prepare(cfg)
	stateFile := filepath.Join(home, "internal", "state.json")
	if err := os.WriteFile(stateFile, []byte("kept"), 0o600); err != nil {
		t.Fatal(err)
	}
	Prepare(cfg)

	b, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatalf("read %s: %v", stateFile, err)
	}
	if string(b) != "kept" {
		t.Errorf("state.json = %q after a second start, want it untouched", b)
	}
	for _, dir := range []string{"internal", "cli", "skills", "common"} {
		assertPrivateDir(t, filepath.Join(home, dir))
	}
}

// agents.common_dir is created instead of <CLAW_HOME>/common.
func TestPrepareCommonDirOverride(t *testing.T) {
	cfg, home := newConfig(t)
	custom := filepath.Join(t.TempDir(), "shared")
	cfg.Agents.CommonDir = custom

	Prepare(cfg)

	assertPrivateDir(t, custom)
	if _, err := os.Lstat(filepath.Join(home, "common")); !os.IsNotExist(err) {
		t.Errorf("%s/common created although agents.common_dir is set (err %v)", home, err)
	}
}
