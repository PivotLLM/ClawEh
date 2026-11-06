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
//	bin/       the operator's scripts and binaries (files there keep their execute bits)
package layout

import (
	"os"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/logger"
)

// Prepare creates internal/, bin/, cli/, skills/, cards/ and the common
// directory under the data directory when they are missing, each with exactly
// its area's folder mode (security.permissions): internal/ the secrets', bin/
// the bin area's, the common directory the common area's, the others the
// default area's. It runs at every start, before anything opens the files
// they hold. An existing directory is left to perms.Enforce. Problems are
// logged; none stops the start.
func Prepare(cfg *config.Config) {
	if cfg.DataDir() == "" {
		return
	}
	dirMode := cfg.PermissionModes(config.AreaDefault).Dir
	for dir, mode := range map[string]os.FileMode{
		cfg.InternalPath():     cfg.PermissionModes(config.AreaSecrets).Dir,
		cfg.BinPath():          cfg.PermissionModes(config.AreaBin).Dir,
		cfg.CLIPath():          dirMode,
		cfg.SkillsPath():       dirMode,
		cfg.CardsPath():        dirMode,
		cfg.ResolveCommonDir(): cfg.PermissionModes(config.AreaCommon).Dir,
	} {
		if _, err := os.Stat(dir); err == nil {
			continue
		}
		if err := os.MkdirAll(dir, mode); err != nil {
			logger.WarnCF("layout", "Cannot create a data directory", map[string]any{"path": dir, "error": err.Error()})
			continue
		}
		// MkdirAll's mode is narrowed by the umask; the folder takes exactly
		// its configured one.
		if err := os.Chmod(dir, mode); err != nil {
			logger.WarnCF("layout", "Cannot set a data directory's mode", map[string]any{"path": dir, "error": err.Error()})
		}
	}
}
