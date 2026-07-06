// ClawEh
// License: MIT

package fusion

import (
	"net/http"
	"path/filepath"
	"sync"

	mcpfusion "github.com/PivotLLM/MCPFusion/fusion"
	"github.com/tenebris-tech/mlogger"

	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/logger"
)

// One shared Fusion engine for the whole process. fusion.New starts a background
// connection-cleanup goroutine, and RegisterTools runs on every registry
// (re)build, so a per-agent/per-rebuild engine would leak goroutines. Per-agent
// isolation is preserved instead by keying token storage on the tenant
// (agentID), passed to ToolSpecDefinitions. Config-folder changes need a restart
// (process-stable metadata, matching MCPFusion).
var (
	engineOnce sync.Once
	engine     *mcpfusion.Fusion
)

// sharedEngine returns the process-wide Fusion engine, building it once from the
// fusion config folder and a shared SQLite-backed DataStore. Returns nil (after
// logging) when the DataStore cannot be opened, so the provider yields no tools.
func sharedEngine(c *config.Config) *mcpfusion.Fusion {
	engineOnce.Do(func() {
		ds, err := NewSQLiteDataStore(c.FusionTokensPath())
		if err != nil {
			logger.ErrorCF("fusion", "failed to open fusion token store; fusion tools disabled",
				map[string]any{"path": c.FusionTokensPath(), "error": err.Error()})
			return
		}

		// Fusion logs to its own file so OAuth device-flow debugging is separable
		// from the main ClawEh log. mlogger.WithLogFile creates the parent dir.
		logPath := filepath.Join(c.FusionPath(), "fusion.log")
		lg, err := mlogger.New(mlogger.WithLogFile(logPath), mlogger.WithPrefix("fusion"))
		if err != nil {
			// A missing log file must not disable the tools; fusion nil-checks its
			// logger, so fall back to no logger rather than failing.
			logger.WarnCF("fusion", "failed to open fusion log; continuing without one",
				map[string]any{"path": logPath, "error": err.Error()})
			lg = nil
		}

		// Option order matters: WithLogger first, because WithDataStore and
		// WithConfigDir use f.logger during construction.
		engine = mcpfusion.New(
			mcpfusion.WithLogger(lg),
			mcpfusion.WithDataStore(ds),
			mcpfusion.WithConfigDir(c.FusionPath()),
			// External URL + command name are advertised to the claw-auth OAuth
			// utility so it can reach this gateway's mounted OAuth API.
			mcpfusion.WithExternalURL(c.Gateway.EffectiveExternalURL()),
			mcpfusion.WithAuthCommandName("claw-auth"),
		)
	})
	return engine
}

// OAuthHandler returns the fusion OAuth API handler (with its built-in
// auth-code -> tenant middleware) for the gateway to mount on the shared HTTP
// server, or nil when the engine could not be built (so no routes are mounted).
func OAuthHandler(c *config.Config) http.Handler {
	e := sharedEngine(c)
	if e == nil {
		return nil
	}
	return e.OAuthHandler()
}
