package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/voice"
)

// registerVoiceRoutes binds speech-to-text configuration endpoints.
func (h *Handler) registerVoiceRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/voice/stt", h.handleGetVoiceSTT)
	mux.HandleFunc("PUT /api/voice/stt", h.handleUpdateVoiceSTT)
}

// handleGetVoiceSTT returns the configured STT backends (API keys masked) plus
// the known provider presets.
//
//	GET /api/voice/stt
func (h *Handler) handleGetVoiceSTT(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}

	out := make([]config.STTProvider, len(cfg.Voice.STT))
	for i, s := range cfg.Voice.STT {
		s.APIKey = maskAPIKey(s.APIKey)
		out[i] = s
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"stt":     out,
		"presets": voice.STTPresets(),
	})
}

// handleUpdateVoiceSTT replaces the STT list. A blank or masked api_key on an
// incoming entry keeps the stored key at the same list position, so the UI can
// round-trip masked values without wiping credentials.
//
//	PUT /api/voice/stt  {"stt": [...]}
func (h *Handler) handleUpdateVoiceSTT(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}

	var body struct {
		STT []config.STTProvider `json:"stt"`
	}
	if err = json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	for i := range body.STT {
		body.STT[i].Provider = strings.TrimSpace(body.STT[i].Provider)
		if body.STT[i].Provider == "" {
			http.Error(w, "each STT entry needs a provider", http.StatusBadRequest)
			return
		}
		key := body.STT[i].APIKey
		if key == "" || strings.Contains(key, "****") {
			if i < len(cfg.Voice.STT) {
				body.STT[i].APIKey = cfg.Voice.STT[i].APIKey
			} else {
				body.STT[i].APIKey = ""
			}
		}
	}

	cfg.Voice.STT = body.STT
	if err = config.SaveConfig(h.configPath, cfg); err != nil {
		http.Error(w, fmt.Sprintf("Failed to save config: %v", err), http.StatusInternalServerError)
		return
	}
	// Reload so the running agent loop re-detects the active transcriber.
	if reload := h.reloadFunc(); reload != nil {
		_ = reload()
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
