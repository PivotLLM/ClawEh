// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package gateway

import (
	"github.com/PivotLLM/ClawEh/pkg/logger"
	spawnlog "github.com/PivotLLM/spawnllm/logger"
)

// spawnllmLogBackend routes spawnllm's provider/dispatch logs into ClawEh's
// logger, so they land in claw.log with the same formatting and rotation as the
// rest of the app. spawnllm is silent until this backend is installed.
type spawnllmLogBackend struct{}

func (spawnllmLogBackend) Log(level, component, message string, fields map[string]any) {
	switch level {
	case "debug":
		logger.DebugCF(component, message, fields)
	case "warn":
		logger.WarnCF(component, message, fields)
	case "error":
		logger.ErrorCF(component, message, fields)
	default:
		logger.InfoCF(component, message, fields)
	}
}

func (spawnllmLogBackend) LogMessageContent() bool { return logger.GetLogMessageContent() }

// installSpawnllmLogging wires spawnllm's logging seam to ClawEh's logger.
func installSpawnllmLogging() { spawnlog.SetBackend(spawnllmLogBackend{}) }
