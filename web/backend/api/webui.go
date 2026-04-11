package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/config"
)

// registerWebUIRoutes binds WebUI Channel management endpoints to the ServeMux.
func (h *Handler) registerWebUIRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/webui/token", h.handleGetWebUIToken)
	mux.HandleFunc("POST /api/webui/token", h.handleRegenWebUIToken)
	mux.HandleFunc("POST /api/webui/setup", h.handleWebUISetup)
}

// handleGetWebUIToken returns the current WS token and URL for the frontend.
//
//	GET /api/webui/token
func (h *Handler) handleGetWebUIToken(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}

	wsURL := h.buildWsURL(r, cfg)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"token":   cfg.Channels.WebUI.Token,
		"ws_url":  wsURL,
		"enabled": cfg.Channels.WebUI.Enabled,
	})
}

// handleRegenWebUIToken generates a new WebUI WebSocket token and saves it.
//
//	POST /api/webui/token
func (h *Handler) handleRegenWebUIToken(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}

	token := generateSecureToken()
	cfg.Channels.WebUI.Token = token

	if err := config.SaveConfig(h.configPath, cfg); err != nil {
		http.Error(w, fmt.Sprintf("Failed to save config: %v", err), http.StatusInternalServerError)
		return
	}

	wsURL := h.buildWsURL(r, cfg)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"token":  token,
		"ws_url": wsURL,
	})
}

// ensureWebUIChannel checks if the WebUI Channel is properly configured and
// enables it with sensible defaults if not. Returns true if config was changed.
func (h *Handler) ensureWebUIChannel() (bool, error) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		return false, fmt.Errorf("failed to load config: %w", err)
	}

	changed := false

	if !cfg.Channels.WebUI.Enabled {
		cfg.Channels.WebUI.Enabled = true
		changed = true
	}

	if cfg.Channels.WebUI.Token == "" {
		cfg.Channels.WebUI.Token = generateSecureToken()
		changed = true
	}

	if !cfg.Channels.WebUI.AllowTokenQuery {
		cfg.Channels.WebUI.AllowTokenQuery = true
		changed = true
	}

	// Make sure origins are allowed (frontend might be running on a different port like 5173 during dev)
	if len(cfg.Channels.WebUI.AllowOrigins) == 0 {
		cfg.Channels.WebUI.AllowOrigins = []string{"*"}
		changed = true
	}

	if changed {
		if err := config.SaveConfig(h.configPath, cfg); err != nil {
			return false, fmt.Errorf("failed to save config: %w", err)
		}
	}

	return changed, nil
}

// handleWebUISetup automatically configures everything needed for the WebUI Channel to work.
//
//	POST /api/webui/setup
func (h *Handler) handleWebUISetup(w http.ResponseWriter, r *http.Request) {
	changed, err := h.ensureWebUIChannel()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}

	wsURL := h.buildWsURL(r, cfg)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"token":   cfg.Channels.WebUI.Token,
		"ws_url":  wsURL,
		"enabled": true,
		"changed": changed,
	})
}

// generateSecureToken creates a random 32-character hex string.
func generateSecureToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Fallback to something pseudo-random if crypto/rand fails
		return fmt.Sprintf("webui_%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
