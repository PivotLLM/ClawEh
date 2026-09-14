// ClawEh - Cognitive Memory
// License: MIT

// Package cogmemhost is the seam between ClawEh and the cogmem module: the
// attachment loader that enforces the file tools' permissions, the logging
// bridge, and the mapping from ClawEh's config onto cogmem's settings. cogmem
// itself never touches the filesystem outside its own store, never logs
// without a backend, and knows nothing of config.json.
package cogmemhost

import (
	cogmemlog "github.com/PivotLLM/cogmem/logger"

	"github.com/PivotLLM/ClawEh/logger"
)

// logBackend routes cogmem's events into ClawEh's logger so they land in
// claw.log with the same formatting and rotation as the rest of the app.
type logBackend struct{}

func (logBackend) Log(level, component, message string, fields map[string]any) {
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

// InstallLogging wires cogmem's logging seam to ClawEh's logger. cogmem is
// silent until this runs; the gateway calls it at startup and tests may call
// it to see cogmem's log lines.
func InstallLogging() { cogmemlog.SetBackend(logBackend{}) }
