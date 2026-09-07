package api

import (
	"net/http"
	"os"
	"runtime"
	"time"

	"github.com/PivotLLM/ClawEh/app"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/internal/pidfile"
)

// processStart is set when the package is initialised, which happens as the
// process starts — close enough to the real thing to report uptime, and it
// avoids threading the health server's clock through to the HTTP handlers.
var processStart = time.Now()

// statusResponse is the runtime picture the Status page renders: what is
// running, for how long, what it costs, and how much is configured.
//
// Everything here is cheap to compute on request. Nothing is cached, because a
// status page that shows a stale number is worse than one that takes a
// millisecond longer.
type statusResponse struct {
	Version string `json:"version"`
	Build   string `json:"build,omitempty"`

	UptimeSeconds int64  `json:"uptime_seconds"`
	Uptime        string `json:"uptime"`

	PID int `json:"pid"`
	// MemoryBytes is resident set size — the physical RAM the process holds.
	// Not virtual size, which for a Go process counts over a gigabyte of
	// reserved address space and would badly misrepresent the number.
	MemoryBytes int64 `json:"memory_bytes"`
	// HeapBytes is what Go itself has in use, which explains how much of the
	// resident figure is the program rather than its mapped binary.
	HeapBytes  int64 `json:"heap_bytes"`
	Goroutines int   `json:"goroutines"`

	Agents    int `json:"agents"`
	Models    int `json:"models"`
	Providers int `json:"providers"`
	Channels  int `json:"channels"`

	// CLIProviders reports whether any enabled model runs a subprocess CLI,
	// which is what makes the MCP host start.
	CLIProviders bool `json:"cli_providers"`
	MCPHost      bool `json:"mcp_host"`
}

func (h *Handler) registerSystemStatusRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/system/status", h.handleSystemStatus)
}

// handleSystemStatus reports the running instance's runtime state.
//
//	GET /api/system/status
func (h *Handler) handleSystemStatus(w http.ResponseWriter, r *http.Request) {
	up := time.Since(processStart).Truncate(time.Second)
	resp := statusResponse{
		Version:       app.Version(),
		UptimeSeconds: int64(up.Seconds()),
		Uptime:        up.String(),
		PID:           os.Getpid(),
		Goroutines:    runtime.NumGoroutine(),
	}
	resp.Build, _ = app.BuildInfo()

	// Reporting on ourselves, so no pid file and no staleness question.
	if rss, ok := pidfile.RSSBytes(os.Getpid()); ok {
		resp.MemoryBytes = rss
	}
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	resp.HeapBytes = int64(ms.HeapInuse)

	// Config counts are best-effort: an unreadable config should still leave a
	// page that answers "is it up, and how big".
	if cfg, err := config.LoadConfig(h.configPath); err == nil {
		resp.Agents = len(cfg.Agents.List)
		resp.Providers = len(cfg.Providers)
		for i := range cfg.Models {
			if cfg.Models[i].Enabled {
				resp.Models++
			}
		}
		resp.Channels = countChannels(cfg)
		resp.CLIProviders = cfg.HasCLIProvider()
		resp.MCPHost = cfg.MCPHostEffectivelyEnabled()
	}

	writeJSON(w, http.StatusOK, resp)
}

// countChannels counts the configured messaging channels. Telegram and secmsg
// are lists (an install may run several bots), the rest are single blocks that
// count when enabled.
func countChannels(cfg *config.Config) int {
	n := len(cfg.Channels.Telegram) + len(cfg.Channels.SecMsg)
	for _, enabled := range []bool{
		cfg.Channels.Discord.Enabled,
		cfg.Channels.Slack.Enabled,
		cfg.Channels.Matrix.Enabled,
		cfg.Channels.LINE.Enabled,
		cfg.Channels.WebUI.Enabled,
		cfg.Channels.Device.Enabled,
	} {
		if enabled {
			n++
		}
	}
	return n
}
