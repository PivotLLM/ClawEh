// Package api: in-process gateway log endpoints.
//
// In the merged claw binary the gateway runs inside the same process as the
// WebUI HTTP handlers, so there is no subprocess to spawn, stop, or
// supervise. Lifecycle endpoints (start/stop/restart/status/events) were
// removed along with the WebUI controls that backed them. The logs endpoint
// tails the unified claw.log the whole process writes to.

package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/PivotLLM/ClawEh/pkg/logger"
)

// Log-tail bounds for the WebUI logs endpoint.
const (
	defaultLogLines = 250
	maxLogLines     = 5000
	// maxTailBytes caps how far back we read from the log file so a large
	// claw.log never forces a full read; sized for the longest realistic lines.
	maxTailBytes = 16 << 20 // 16 MiB
	bytesPerLine = 1024     // budget estimate for tail sizing
)

// registerGatewayRoutes binds gateway log endpoints to the ServeMux.
func (h *Handler) registerGatewayRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/gateway/logs", h.handleGatewayLogs)
	mux.HandleFunc("POST /api/gateway/reload", h.handleGatewayReload)
}

// handleGatewayReload forces an immediate config reload, bypassing the
// mtime-debounce so WebUI changes (e.g. finishing the setup wizard) take effect
// at once instead of ~10-15s later. Blocks until the reload completes.
//
//	POST /api/gateway/reload
func (h *Handler) handleGatewayReload(w http.ResponseWriter, r *http.Request) {
	fn := h.reloadFunc()
	if fn == nil {
		http.Error(w, "reload is not available in this process", http.StatusServiceUnavailable)
		return
	}
	if err := fn(); err != nil {
		http.Error(w, fmt.Sprintf("reload failed: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "reloaded"})
}

// handleGatewayLogs returns the last N lines of the unified claw.log, newest
// last. N comes from the "lines" query param (default 250, capped at 5000).
//
//	GET /api/gateway/logs?lines=250
func (h *Handler) handleGatewayLogs(w http.ResponseWriter, r *http.Request) {
	n := defaultLogLines
	if raw := r.URL.Query().Get("lines"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			n = v
		}
	}
	if n > maxLogLines {
		n = maxLogLines
	}

	w.Header().Set("Content-Type", "application/json")
	// Live data: never serve a cached copy, so the Refresh button always reflects
	// the current log rather than a browser-cached response for this URL.
	w.Header().Set("Cache-Control", "no-store")
	path := logger.GetLogFilePath()
	if path == "" {
		json.NewEncoder(w).Encode(map[string]any{
			"logs":  []string{},
			"count": 0,
			"error": "file logging is disabled",
		})
		return
	}

	lines, err := tailLines(path, n)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]any{
			"logs":  []string{},
			"count": 0,
			"error": err.Error(),
		})
		return
	}
	json.NewEncoder(w).Encode(map[string]any{
		"logs":  lines,
		"count": len(lines),
	})
}

// tailLines returns the last n lines of the file at path, reading only a bounded
// window from the end so a large log never forces a full read.
func tailLines(path string, n int) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := fi.Size()

	budget := int64(n) * bytesPerLine
	if budget > maxTailBytes {
		budget = maxTailBytes
	}
	start := size - budget
	if start < 0 {
		start = 0
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}

	trimmed := strings.TrimRight(string(data), "\n")
	if trimmed == "" {
		return []string{}, nil
	}
	lines := strings.Split(trimmed, "\n")
	// When we started mid-file the first line is a partial; drop it.
	if start > 0 && len(lines) > 1 {
		lines = lines[1:]
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, nil
}
