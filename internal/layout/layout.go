// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

// Package layout prepares the data directory (CLAW_HOME) at start by creating
// the directories claw owns.
//
// The layout, by purpose:
//
//	internal/  claw's own state nobody edits (token stores, the device database, audit.db, claw.pid, claw.lock)
//	cli/       working directory for CLI providers whose model sets no workspace
//	skills/    shared skills every agent can use
//	common/    shared directory for the common_* tools (unless agents.common_dir is set)
package layout

import (
	"os"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/logger"
)

// Prepare creates internal/, cli/, skills/, cards/ and the common directory
// under the data directory when they are missing. It runs at every start, before
// anything opens the files they hold. internal/ holds secrets and is always
// owner-only; cli/, skills/ and cards/ take the default directory mode and the
// common directory the common area's (security.permissions). Problems are
// logged; none stops the start.
func Prepare(cfg *config.Config) {
	if cfg.DataDir() == "" {
		return
	}
	dirMode := cfg.PermissionModes("").Dir
	for dir, mode := range map[string]os.FileMode{
		cfg.InternalPath():     config.DefaultDirMode,
		cfg.CLIPath():          dirMode,
		cfg.SkillsPath():       dirMode,
		cfg.CardsPath():        dirMode,
		cfg.ResolveCommonDir(): cfg.PermissionModes(config.AreaCommon).Dir,
	} {
		if err := os.MkdirAll(dir, mode); err != nil {
			logger.WarnCF("layout", "Cannot create a data directory", map[string]any{"path": dir, "error": err.Error()})
		}
	}
}
