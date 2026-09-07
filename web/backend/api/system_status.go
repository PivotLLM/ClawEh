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
	//
	// The Go heap is deliberately not reported alongside it. It is a subset of
	// this figure, usually a small one (the mapped binary dominates), and this
	// page is for "how big is it", not for diagnosing the allocator.
	MemoryBytes int64 `json:"memory_bytes"`
	Goroutines  int   `json:"goroutines"`

	// GoVersion and the platform fields identify what this binary is, which is
	// the first thing anyone asks for in a bug report.
	GoVersion string `json:"go_version,omitempty"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	// OSName is a human name for the operating system ("Ubuntu 24.04.4 LTS",
	// "macOS"), best-effort and empty when the host does not say.
	OSName string `json:"os_name,omitempty"`

	Agents int `json:"agents"`
	// Models counts enabled models, and Providers counts providers that are
	// actually usable — the same rule the Providers page draws its green dot
	// from. Totals including disabled and unconfigured entries would answer a
	// question nobody is asking of a status page.
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
	resp.Build, resp.GoVersion = app.BuildInfo()
	resp.OS, resp.Arch = runtime.GOOS, runtime.GOARCH
	resp.OSName = osName()

	// Reporting on ourselves, so no pid file and no staleness question.
	if rss, ok := pidfile.RSSBytes(os.Getpid()); ok {
		resp.MemoryBytes = rss
	}
	// Config counts are best-effort: an unreadable config should still leave a
	// page that answers "is it up, and how big".
	if cfg, err := config.LoadConfig(h.configPath); err == nil {
		resp.Agents = len(cfg.Agents.List)
		for i := range cfg.Providers {
			if providerReady(&cfg.Providers[i]) {
				resp.Providers++
			}
		}
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
