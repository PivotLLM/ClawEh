package api

import (
	"net/http"

	"github.com/PivotLLM/ClawEh/pkg/global"
)

// registerVersionRoutes exposes the running ClawEh build version so the WebUI can
// display it (e.g. in the sidebar footer).
func (h *Handler) registerVersionRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/system/version", h.handleVersion)
}

func (h *Handler) handleVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"version": global.Version})
}
