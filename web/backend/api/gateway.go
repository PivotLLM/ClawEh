// Package api: in-process gateway log endpoints.
//
// In the merged claw binary the gateway runs inside the same process as the
// WebUI HTTP handlers, so there is no subprocess to spawn, stop, or
// supervise. Lifecycle endpoints (start/stop/restart/status/events) were
// removed along with the WebUI controls that backed them. Only the log
// endpoints remain for the Logs page; they currently return empty payloads
// because the merged binary writes to a unified log file rather than buffering
// a subprocess pipe.

package api

import (
	"encoding/json"
	"net/http"
)

// registerGatewayRoutes binds gateway log endpoints to the ServeMux.
func (h *Handler) registerGatewayRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/gateway/logs", h.handleGatewayLogs)
	mux.HandleFunc("POST /api/gateway/logs/clear", h.handleGatewayClearLogs)
}

// handleGatewayClearLogs is a no-op in the merged binary (logs are emitted to
// the same logger as the rest of the process, not a subprocess pipe buffer).
//
//	POST /api/gateway/logs/clear
func (h *Handler) handleGatewayClearLogs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status":     "cleared",
		"log_total":  0,
		"log_run_id": 0,
	})
}

// handleGatewayLogs returns an empty log payload. The merged binary no longer
// keeps a separate log buffer for the gateway subprocess; consumers should
// read the unified claw log file directly.
//
//	GET /api/gateway/logs
func (h *Handler) handleGatewayLogs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"logs":       []string{},
		"log_total":  0,
		"log_run_id": 0,
	})
}
