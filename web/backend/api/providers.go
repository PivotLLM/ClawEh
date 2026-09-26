package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/PivotLLM/ClawEh/config"
)

// registerProviderRoutes binds named-provider management endpoints.
func (h *Handler) registerProviderRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/providers", h.handleListProviders)
	mux.HandleFunc("POST /api/providers/test", h.handleTestProvider)
	mux.HandleFunc("POST /api/providers", h.handleAddProvider)
	mux.HandleFunc("PUT /api/providers/{index}", h.handleUpdateProvider)
	mux.HandleFunc("DELETE /api/providers/{index}", h.handleDeleteProvider)
}

type providerResponse struct {
	Index        int    `json:"index"`
	Name         string `json:"name"`
	Protocol     string `json:"protocol"`
	BaseURL      string `json:"base_url,omitempty"`
	APIKey       string `json:"api_key"`
	Proxy        string `json:"proxy,omitempty"`
	StrictCompat bool   `json:"strict_compat,omitempty"`
	// RequireReasoningContent is the inverse of StrictCompat: some endpoints
	// (DeepSeek V4 thinking mode) reject history that is MISSING
	// reasoning_content rather than history that carries it.
	RequireReasoningContent bool   `json:"require_reasoning_content,omitempty"`
	NoParallelToolCalls     bool   `json:"no_parallel_tool_calls,omitempty"`
	ResponseFormatJSON      bool   `json:"response_format_json,omitempty"`
	Command                 string `json:"command,omitempty"`
	// Ready is whether the provider is usable as configured: an API key for
	// HTTP providers, a binary that actually resolves for CLI ones. The check
	// belongs here rather than in the browser — only this process knows the
	// PATH its CLI subprocesses will be launched with.
	Ready bool `json:"ready"`
	// ResolvedCommand is where a CLI provider's binary was found, so the card
	// can say which one it will run when the command field is left blank.
	ResolvedCommand string `json:"resolved_command,omitempty"`
	// ModelCount is how many models entries reference this provider — used
	// by the WebUI to warn before deleting an in-use provider.
	ModelCount int `json:"model_count"`
}

func (h *Handler) handleListProviders(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.currentConfig()
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
			Index:                   i,
			Name:                    p.Name,
			Protocol:                p.Protocol,
			BaseURL:                 p.BaseURL,
			APIKey:                  maskAPIKey(p.APIKey),
			Proxy:                   maskProxyURL(p.Proxy),
			StrictCompat:            p.StrictCompat,
			RequireReasoningContent: p.RequireReasoningContent,
			NoParallelToolCalls:     p.NoParallelToolCalls,
			ResponseFormatJSON:      p.ResponseFormatJSON,
			Command:                 p.Command,
			Ready:                   providerReady(p),
			ResolvedCommand:         resolvedCLICommand(p),
			ModelCount:              counts[p.Name],
		})
	}

	w.Header().Set("Content-Type", "application/json")
	encodeJSON(w, map[string]any{"providers": out, "total": len(out)})
}

func (h *Handler) handleAddProvider(w http.ResponseWriter, r *http.Request) {
	var p config.Provider
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	index := -1
	err := h.updateConfig(func(cfg *config.Config) error {
		cfg.Providers = append(cfg.Providers, p)
		index = len(cfg.Providers) - 1
		// Validate only the new provider, not the whole list — pre-existing invalid
		// entries (e.g. a stale protocol awaiting migration) must not block adding a
		// valid one.
		if err := cfg.ValidateProvider(index); err != nil {
			return badRequest("Validation error: %v", err)
		}
		return nil
	})
	if err != nil {
		writeUpdateError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	encodeJSON(w, map[string]any{"status": "ok", "index": index})
}

func (h *Handler) handleUpdateProvider(w http.ResponseWriter, r *http.Request) {
	idx, err := strconv.Atoi(r.PathValue("index"))
	if err != nil {
		http.Error(w, "Invalid index", http.StatusBadRequest)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}

	err = h.updateConfig(func(cfg *config.Config) error {
		if idx < 0 || idx >= len(cfg.Providers) {
			return notFound("Index %d out of range", idx)
		}

		// Start from the existing entry so omitted fields keep their value.
		p := cfg.Providers[idx]
		oldName := p.Name
		if decErr := json.Unmarshal(body, &p); decErr != nil {
			return badRequest("Invalid JSON: %v", decErr)
		}
		// An empty or masked API key means "keep the stored key"; a masked proxy
		// URL (its userinfo shown as ****) likewise keeps the stored one.
		if p.APIKey == "" || strings.Contains(p.APIKey, "****") {
			p.APIKey = cfg.Providers[idx].APIKey
		}
		if isMasked(p.Proxy) {
			p.Proxy = cfg.Providers[idx].Proxy
		}
		cfg.Providers[idx] = p

		// Validate only the edited provider, not the whole list — this lets an
		// operator repair entries one at a time even while others are still invalid
		// (e.g. migrating several providers off a renamed protocol).
		if verr := cfg.ValidateProvider(idx); verr != nil {
			return badRequest("Validation error: %v", verr)
		}

		// If the provider was renamed, re-point models that referenced it.
		if p.Name != oldName {
			for i := range cfg.Models {
				if cfg.Models[i].Provider == oldName {
					cfg.Models[i].Provider = p.Name
				}
			}
		}
		return nil
	})
	if err != nil {
		writeUpdateError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	encodeJSON(w, map[string]string{"status": "ok"})
}

func (h *Handler) handleDeleteProvider(w http.ResponseWriter, r *http.Request) {
	idx, err := strconv.Atoi(r.PathValue("index"))
	if err != nil {
		http.Error(w, "Invalid index", http.StatusBadRequest)
		return
	}

	err = h.updateConfig(func(cfg *config.Config) error {
		if idx < 0 || idx >= len(cfg.Providers) {
			return notFound("Index %d out of range", idx)
		}

		name := cfg.Providers[idx].Name
		for _, m := range cfg.Models {
			if m.Provider == name {
				return &httpError{status: http.StatusConflict, msg: fmt.Sprintf("provider %q is in use by model %q", name, m.ModelName)}
			}
		}

		cfg.Providers = append(cfg.Providers[:idx], cfg.Providers[idx+1:]...)
		return nil
	})
	if err != nil {
		writeUpdateError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	encodeJSON(w, map[string]string{"status": "ok"})
}
