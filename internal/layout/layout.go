// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

// Package layout prepares the data directory (CLAW_HOME) at start by creating
// the directories claw owns.
//
// The layout, by purpose:
//
//	internal/  claw's own state nobody edits (state.json, token stores, the device database, audit.db, claw.pid, claw.lock)
//	cli/       working directory for CLI providers whose model sets no workspace
//	skills/    shared skills every agent can use
//	common/    shared directory for the common_* tools (unless agents.common_dir is set)
package layout

import (
	"os"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/logger"
)

const dirMode = 0o700

// Prepare creates internal/, cli/, skills/ and the common directory under the
// data directory when they are missing. It runs at every start, before
// anything opens the files they hold. Problems are logged; none stops the
// start.
func Prepare(cfg *config.Config) {
	if cfg.DataDir() == "" {
		return
	}
	for _, dir := range []string{cfg.InternalPath(), cfg.CLIPath(), cfg.SkillsPath(), cfg.ResolveCommonDir()} {
		if err := os.MkdirAll(dir, dirMode); err != nil {
			logger.WarnCF("layout", "Cannot create a data directory", map[string]any{"path": dir, "error": err.Error()})
		}
	}
}
