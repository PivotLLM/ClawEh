package api

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"

	"github.com/PivotLLM/ClawEh/config"
)

// registerWebUIRoutes binds WebUI Channel management endpoints to the ServeMux.
//
// The browser no longer fetches the channel token: /webui/ws accepts the login
// session cookie, so the token stays on the server and is only for non-browser
// clients that present it as a Bearer header.
func (h *Handler) registerWebUIRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/webui/setup", h.handleWebUISetup)
}

// EnsureWebUIChannel checks if the WebUI Channel is properly configured and
// enables it with sensible defaults if not. Returns true if config was changed.
func (h *Handler) EnsureWebUIChannel() (bool, error) {
	changed := false
	err := h.updateConfig(func(cfg *config.Config) error {
		if !cfg.Channels.WebUI.Enabled {
			cfg.Channels.WebUI.Enabled = true
			changed = true
		}

		if cfg.Channels.WebUI.Token == "" {
			cfg.Channels.WebUI.Token = generateSecureToken()
			changed = true
		}

		// Without allow_from, IsAllowedSender returns false and silently drops every message.
		if len(cfg.Channels.WebUI.AllowFrom) == 0 {
			cfg.Channels.WebUI.AllowFrom = config.FlexibleStringSlice{"*"}
			changed = true
		}

		if !changed {
			return config.ErrUnchanged
		}
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("failed to save config: %w", err)
	}
	return changed, nil
}

// handleWebUISetup automatically configures everything needed for the WebUI Channel to work.
//
//	POST /api/webui/setup → {"enabled":true,"changed":bool}
func (h *Handler) handleWebUISetup(w http.ResponseWriter, r *http.Request) {
	changed, err := h.EnsureWebUIChannel()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	encodeJSON(w, map[string]any{
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
