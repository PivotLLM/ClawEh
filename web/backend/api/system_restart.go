// ClawEh
// License: MIT

package api

import (
	"net/http"
	"os"

	"github.com/PivotLLM/ClawEh/internal/audit"
)

// underServiceManager reports whether a service manager will start the
// process again after it exits non-zero. systemd sets INVOCATION_ID for
// every unit it runs, and the units `claw install` writes use
// Restart=on-failure.
func underServiceManager() bool {
	return os.Getenv("INVOCATION_ID") != ""
}

// SetRestart wires the gateway's clean-shutdown-and-exit path into the
// handler so POST /api/system/restart can restart the process.
func (h *Handler) SetRestart(fn func()) {
	h.reloadMu.Lock()
	h.restartHook = fn
	h.reloadMu.Unlock()
}

func (h *Handler) restartFunc() func() {
	h.reloadMu.Lock()
	defer h.reloadMu.Unlock()
	return h.restartHook
}

func (h *Handler) serviceManaged() bool {
	h.reloadMu.Lock()
	fn := h.serviceManagedFn
	h.reloadMu.Unlock()
	if fn == nil {
		return underServiceManager()
	}
	return fn()
}

func (h *Handler) registerSystemRestartRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/system/restart", h.handleSystemRestart)
}

// handleSystemRestart restarts the gateway when a service manager will bring
// it back: 202 and a clean shutdown with a non-zero exit. Without a service
// manager nothing happens and the answer is 409, so the operator restarts
// ClawEh by hand instead of being left with a dead process.
//
//	POST /api/system/restart
func (h *Handler) handleSystemRestart(w http.ResponseWriter, r *http.Request) {
	restart := h.restartFunc()
	if restart == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "restart is not available in this process")
		return
	}
	if !h.serviceManaged() {
		writeJSONError(w, http.StatusConflict, "not running as a service; restart ClawEh by hand")
		return
	}
	audit.Default().Record(audit.Event{
		Kind:    audit.KindRestart,
		Actor:   audit.ActorFromContext(r.Context()),
		Sender:  clientIP(r),
		Summary: "restart requested",
		Outcome: audit.OutcomeOK,
	})
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "restarting"})
	restart()
}
