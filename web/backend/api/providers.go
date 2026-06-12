package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/PivotLLM/ClawEh/pkg/config"
)

// registerProviderRoutes binds named-provider management endpoints.
func (h *Handler) registerProviderRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/providers", h.handleListProviders)
	mux.HandleFunc("POST /api/providers", h.handleAddProvider)
	mux.HandleFunc("PUT /api/providers/{index}", h.handleUpdateProvider)
	mux.HandleFunc("DELETE /api/providers/{index}", h.handleDeleteProvider)
}

type providerResponse struct {
	Index               int    `json:"index"`
	Name                string `json:"name"`
	Protocol            string `json:"protocol"`
	BaseURL             string `json:"base_url,omitempty"`
	APIKey              string `json:"api_key"`
	Proxy               string `json:"proxy,omitempty"`
	AuthMethod          string `json:"auth_method,omitempty"`
	StrictCompat        bool   `json:"strict_compat,omitempty"`
	NoParallelToolCalls bool   `json:"no_parallel_tool_calls,omitempty"`
	ResponseFormatJSON  bool   `json:"response_format_json,omitempty"`
	StrictAlternation   bool   `json:"strict_alternation,omitempty"`
	Command             string `json:"command,omitempty"`
	// ModelCount is how many models entries reference this provider — used
	// by the WebUI to warn before deleting an in-use provider.
	ModelCount int `json:"model_count"`
}

func (h *Handler) handleListProviders(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}

	counts := map[string]int{}
	for _, m := range cfg.Models {
		counts[m.Provider]++
	}

	out := make([]providerResponse, 0, len(cfg.Providers))
	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		out = append(out, providerResponse{
			Index:               i,
			Name:                p.Name,
			Protocol:            p.Protocol,
			BaseURL:             p.BaseURL,
			APIKey:              maskAPIKey(p.APIKey),
			Proxy:               p.Proxy,
			AuthMethod:          p.AuthMethod,
			StrictCompat:        p.StrictCompat,
			NoParallelToolCalls: p.NoParallelToolCalls,
			ResponseFormatJSON:  p.ResponseFormatJSON,
			StrictAlternation:   p.StrictAlternation,
			Command:             p.Command,
			ModelCount:          counts[p.Name],
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"providers": out, "total": len(out)})
}

func (h *Handler) handleAddProvider(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}

	var p config.Provider
	if err = json.NewDecoder(r.Body).Decode(&p); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	cfg.Providers = append(cfg.Providers, p)
	if err = cfg.ValidateProviders(); err != nil {
		http.Error(w, fmt.Sprintf("Validation error: %v", err), http.StatusBadRequest)
		return
	}

	if err = config.SaveConfig(h.configPath, cfg); err != nil {
		http.Error(w, fmt.Sprintf("Failed to save config: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"status": "ok", "index": len(cfg.Providers) - 1})
}

func (h *Handler) handleUpdateProvider(w http.ResponseWriter, r *http.Request) {
	idx, err := strconv.Atoi(r.PathValue("index"))
	if err != nil {
		http.Error(w, "Invalid index", http.StatusBadRequest)
		return
	}

	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}
	if idx < 0 || idx >= len(cfg.Providers) {
		http.Error(w, fmt.Sprintf("Index %d out of range", idx), http.StatusNotFound)
		return
	}

	// Start from the existing entry so omitted fields keep their value.
	p := cfg.Providers[idx]
	oldName := p.Name
	if err = json.NewDecoder(r.Body).Decode(&p); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}
	// An empty or masked API key means "keep the stored key".
	if p.APIKey == "" || strings.Contains(p.APIKey, "****") {
		p.APIKey = cfg.Providers[idx].APIKey
	}
	cfg.Providers[idx] = p

	if err = cfg.ValidateProviders(); err != nil {
		http.Error(w, fmt.Sprintf("Validation error: %v", err), http.StatusBadRequest)
		return
	}

	// If the provider was renamed, re-point models that referenced it.
	if p.Name != oldName {
		for i := range cfg.Models {
			if cfg.Models[i].Provider == oldName {
				cfg.Models[i].Provider = p.Name
			}
		}
	}

	if err = config.SaveConfig(h.configPath, cfg); err != nil {
		http.Error(w, fmt.Sprintf("Failed to save config: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (h *Handler) handleDeleteProvider(w http.ResponseWriter, r *http.Request) {
	idx, err := strconv.Atoi(r.PathValue("index"))
	if err != nil {
		http.Error(w, "Invalid index", http.StatusBadRequest)
		return
	}

	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}
	if idx < 0 || idx >= len(cfg.Providers) {
		http.Error(w, fmt.Sprintf("Index %d out of range", idx), http.StatusNotFound)
		return
	}

	name := cfg.Providers[idx].Name
	for _, m := range cfg.Models {
		if m.Provider == name {
			http.Error(w, fmt.Sprintf("provider %q is in use by model %q", name, m.ModelName), http.StatusConflict)
			return
		}
	}

	cfg.Providers = append(cfg.Providers[:idx], cfg.Providers[idx+1:]...)
	if err = config.SaveConfig(h.configPath, cfg); err != nil {
		http.Error(w, fmt.Sprintf("Failed to save config: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
