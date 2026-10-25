package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/voice"
)

// registerVoiceRoutes binds speech-to-text configuration endpoints.
func (h *Handler) registerVoiceRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/voice/stt", h.handleGetVoiceSTT)
	mux.HandleFunc("PUT /api/voice/stt", h.handleUpdateVoiceSTT)
}

// sttProviderOption is a configured provider a speech backend may name.
type sttProviderOption struct {
	Name         string `json:"name"`
	DefaultModel string `json:"default_model"`
	Ready        bool   `json:"ready"` // the provider has an API key
}

// handleGetVoiceSTT returns the configured STT backends plus the providers they
// may name: every provider that can transcribe, in config order. No API keys
// are returned.
//
//	GET /api/voice/stt
func (h *Handler) handleGetVoiceSTT(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.currentConfig()
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}

	stt := cfg.Voice.STT
	if stt == nil {
		stt = []config.STTProvider{}
	}
	providers := []sttProviderOption{}
	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		if !voice.CanTranscribe(p) {
			continue
		}
		providers = append(providers, sttProviderOption{
			Name:         p.Name,
			DefaultModel: voice.DefaultSTTModel(p.BaseURL),
			Ready:        p.APIKey != "",
		})
	}

	w.Header().Set("Content-Type", "application/json")
	encodeJSON(w, map[string]any{
		"stt":       stt,
		"providers": providers,
	})
}

// handleUpdateVoiceSTT replaces the STT list. Each entry must name a
// configured provider that can transcribe.
//
//	PUT /api/voice/stt  {"stt": [...]}
func (h *Handler) handleUpdateVoiceSTT(w http.ResponseWriter, r *http.Request) {
	var body struct {
		STT []config.STTProvider `json:"stt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}
	for i := range body.STT {
		body.STT[i].Provider = strings.TrimSpace(body.STT[i].Provider)
		body.STT[i].Model = strings.TrimSpace(body.STT[i].Model)
		if body.STT[i].Provider == "" {
			http.Error(w, "each STT entry needs a provider", http.StatusBadRequest)
			return
		}
	}

	err := h.updateConfig(r, func(cfg *config.Config) error {
		for i := range body.STT {
			p, err := cfg.GetProvider(body.STT[i].Provider)
			if err != nil {
				return badRequest("unknown provider %q", body.STT[i].Provider)
			}
			if !voice.CanTranscribe(p) {
				return badRequest("provider %q cannot transcribe", body.STT[i].Provider)
			}
		}
		cfg.Voice.STT = body.STT
		return nil
	})
	if err != nil {
		writeUpdateError(w, err)
		return
	}
	// Reload so the running agent loop re-detects the active transcriber.
	if reload := h.reloadFunc(); reload != nil {
		if reloadErr := reload(); reloadErr != nil {
			logger.WarnCF("api", "config reload failed", map[string]any{"error": reloadErr.Error()})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	encodeJSON(w, map[string]string{"status": "ok"})
}
