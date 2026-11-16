package api

import (
	"net/http"

	"github.com/PivotLLM/ClawEh/app"
)

// registerVersionRoutes exposes the running build's version and the product's
// names so the WebUI can display them (e.g. in the sidebar footer).
func (h *Handler) registerVersionRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/system/version", h.handleVersion)
}

// handleVersion answers with the version and the product names:
//
//	GET /api/system/version → {"version":"…","name":"…","short_name":"…","binary_name":"…"}
func (h *Handler) handleVersion(w http.ResponseWriter, r *http.Request) {
	out := productNames()
	out["version"] = app.Version()
	writeJSON(w, http.StatusOK, out)
}

// productNames is the product's names as the WebUI uses them: name to
// identify it (title, footer), short_name in sentences, binary_name in
// commands it tells the operator to type. The login page reads them from
// /api/auth/status, which answers before login.
func productNames() map[string]string {
	return map[string]string{
		"name":        app.Name(),
		"short_name":  app.ShortName(),
		"binary_name": app.BinaryName(),
	}
}
