//go:build !windows

// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package layout

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
)

// With security.permissions set, every folder Prepare creates takes exactly
// its area's mode, even under a umask that would narrow it: internal/ the
// secrets', bin/ the bin area's, cli/, skills/ and cards/ the default area's
// and common/ the common area's.
func TestPrepareConfiguredModes(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	cfg, home := newConfig(t)
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg.Security.Permissions = config.PermissionsConfig{
		Default: config.AreaPermissions{DirMode: "0750"},
		Secrets: config.AreaPermissions{DirMode: "0710"},
		Bin:     config.AreaPermissions{DirMode: "0755"},
		Common:  config.AreaPermissions{DirMode: "0770"},
	}

	Prepare(cfg)

	for dir, want := range map[string]os.FileMode{
		"internal": 0o710, "bin": 0o755, "cli": 0o750, "skills": 0o750, "cards": 0o750, "common": 0o770,
	} {
		fi, err := os.Stat(filepath.Join(home, dir))
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("%s mode = %04o, want %04o", dir, got, want)
		}
	}
}
