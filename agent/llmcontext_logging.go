// ClawEh
// License: MIT

package agent

import (
	llmlogger "github.com/PivotLLM/ctxengine/logger"

	"github.com/PivotLLM/ClawEh/logger"
)

// llmcontextLogBackend routes the context engine's structured log events into
// ClawEh's logger, level for level, keeping the engine's own component tags.
type llmcontextLogBackend struct{}

func (llmcontextLogBackend) Log(level, component, message string, fields map[string]any) {
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

// InstallLogging connects the context engine (llmcontext, memory, session) to
// ClawEh's logger. Until it runs the engine is silent. Called once from
// NewAgentLoop; safe to call again.
func InstallLogging() {
	llmlogger.SetBackend(llmcontextLogBackend{})
}
