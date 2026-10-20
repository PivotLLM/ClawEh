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

// With security.permissions set, cli/, skills/ and cards/ take the default
// directory mode and common/ the common area's, while internal/ stays
// owner-only.
func TestPrepareConfiguredModes(t *testing.T) {
	old := syscall.Umask(0)
	defer syscall.Umask(old)
	cfg, home := newConfig(t)
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg.Security.Permissions = config.PermissionsConfig{
		DirMode: "0750",
		Common:  config.AreaPermissions{DirMode: "0770"},
	}

	Prepare(cfg)

	for dir, want := range map[string]os.FileMode{
		"internal": 0o700, "cli": 0o750, "skills": 0o750, "cards": 0o750, "common": 0o770,
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
