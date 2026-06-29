package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/PivotLLM/ClawEh/pkg/config"
)

type setupStatusResponse struct {
	// Pristine is true for an auto-seeded config the user has never saved.
	Pristine bool `json:"pristine"`
	// HasUsableModel is true when at least one enabled model has the credentials
	// (or local/CLI provider) needed to run.
	HasUsableModel bool `json:"has_usable_model"`
	// NeedsSetup drives the first-run redirect to the wizard. It is simply "no
	// usable model": without one the app can't be used, so onboarding is needed.
	// It deliberately does NOT depend on "pristine" — changing an unrelated
	// setting (e.g. the bind address on a headless host) must not suppress
	// onboarding. The WebUI's session "dismissed" flag lets a user opt out.
	NeedsSetup bool `json:"needs_setup"`
}

func (h *Handler) registerSetupStatusRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/system/setup-status", h.handleSetupStatus)
}

func (h *Handler) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}

	resp := setupStatusResponse{Pristine: cfg.DefaultConfig}
	for i := range cfg.Models {
		m := cfg.Models[i]
		// Only enabled models count — the seeded catalog ships disabled entries
		// (incl. always-"configured" CLI models), which must not look usable.
		if !m.Enabled {
			continue
		}
		prov, perr := cfg.GetProvider(m.Provider)
		if perr != nil {
			continue
		}
		if hasModelConfiguration(prov, m) {
			resp.HasUsableModel = true
			break
		}
	}
	resp.NeedsSetup = !resp.HasUsableModel

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
