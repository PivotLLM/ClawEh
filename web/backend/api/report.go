// ClawEh
// License: MIT

package api

import (
	"fmt"
	"net/http"
	"os"
	"os/user"
	"runtime"
	"time"

	"github.com/PivotLLM/ClawEh/app"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/report"
)

func (h *Handler) registerReportRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/report/pdf", h.handleReportPDF)
}

// handleReportPDF streams the configuration report for this install: the
// configuration as loaded plus facts about the running process. Served inline
// so the browser renders it, and never cached.
func (h *Handler) handleReportPDF(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, "Failed to load config: "+err.Error(), http.StatusInternalServerError)
		return
	}
	now := time.Now()
	rep := report.Collect(r.Context(), cfg, reportEnvironment(h.configPath, cfg.DataDir(), now))

	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`inline; filename="claweh-report-%s.pdf"`, now.Format("2006-01-02-1504")))
	w.Header().Set("Cache-Control", "no-store")
	if err := report.RenderPDF(rep, w); err != nil {
		// Headers may already be out; all that is left is to record it.
		logger.ErrorCF("api", "configuration report render failed", map[string]any{"error": err.Error()})
	}
}

// reportEnvironment gathers what the report needs to know about the process.
// Every lookup degrades to "unknown" rather than failing the report.
func reportEnvironment(configPath, dataDir string, now time.Time) report.Environment {
	env := report.Environment{
		ConfigPath: configPath,
		DataDir:    dataDir,
		OS:         runtime.GOOS,
		Arch:       runtime.GOARCH,
		Version:    app.Version(),
		Now:        now,
	}
	env.BuildTime, env.GoVersion = app.BuildInfo()
	if exe, err := os.Executable(); err == nil {
		env.Executable = exe
	} else {
		env.Executable = "unknown"
	}
	if host, err := os.Hostname(); err == nil {
		env.Hostname = host
	} else {
		env.Hostname = "unknown"
	}
	env.User, env.Group = "unknown", "unknown"
	if u, err := user.Current(); err == nil {
		env.User = u.Username
		if g, err := user.LookupGroupId(u.Gid); err == nil {
			env.Group = g.Name
		} else {
			env.Group = u.Gid
		}
	}
	return env
}
