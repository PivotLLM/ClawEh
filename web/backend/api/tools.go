package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/tools"
)

// staticToolDescriptors aliases the canonical list in pkg/tools so the gateway
// and the API layer always agree on which tools are default-enabled.
var staticToolDescriptors = tools.StaticToolDescriptors

type toolSupportItem struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Category    string `json:"category"`
	ConfigKey   string `json:"config_key"`
	Status      string `json:"status"`
	ReasonCode  string `json:"reason_code,omitempty"`
}

type toolSupportResponse struct {
	Tools []toolSupportItem `json:"tools"`
}

type toolStateRequest struct {
	Enabled bool `json:"enabled"`
}

func (h *Handler) registerToolRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/tools", h.handleListTools)
	mux.HandleFunc("PUT /api/tools/{name}/state", h.handleUpdateToolState)
}

func (h *Handler) handleListTools(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(toolSupportResponse{Tools: buildToolSupport(cfg)})
}

func (h *Handler) handleUpdateToolState(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}
	var req toolStateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}
	if err := applyToolState(cfg, r.PathValue("name"), req.Enabled); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := config.SaveConfig(h.configPath, cfg); err != nil {
		http.Error(w, fmt.Sprintf("Failed to save config: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func buildToolSupport(cfg *config.Config) []toolSupportItem {
	var items []toolSupportItem

	for _, p := range tools.GetProviders() {
		_, unavailReason := p.Available(cfg)
		for _, d := range p.Describe() {
			// All-or-nothing suites (cogmem, maestro) are per-agent toggles, not
			// globally enable/disable-able tools — keep them off this page.
			if d.Suite != "" {
				continue
			}
			items = append(items, toolSupportItem{
				Name:        d.Name,
				Description: d.Description,
				Category:    d.Category,
				ConfigKey:   d.ConfigKey,
				Status:      resolveToolStatus(cfg, d, p, unavailReason),
				ReasonCode:  resolveToolReasonCode(cfg, d, p, unavailReason),
			})
		}
	}

	// Append static (non-provider) tools.
	for _, d := range staticToolDescriptors {
		status, reason := resolveStaticToolStatus(cfg, d)
		items = append(items, toolSupportItem{
			Name:        d.Name,
			Description: d.Description,
			Category:    d.Category,
			ConfigKey:   d.ConfigKey,
			Status:      status,
			ReasonCode:  reason,
		})
	}

	return items
}

// resolveToolStatus derives the GUI status from the provider availability and the
// per-tool enabled state. Capability/environment gates (skills registry, subagent,
// Linux-only hardware) are reported by each provider's Available as unavailReason.
func resolveToolStatus(cfg *config.Config, d tools.ToolDescriptor, _ tools.ToolProvider, unavailReason string) string {
	if unavailReason != "" {
		return "blocked"
	}
	if cfg.Tools.ToolEnabled(d.ConfigKey, d.DefaultEnabled) {
		return "enabled"
	}
	return "disabled"
}

func resolveToolReasonCode(_ *config.Config, _ tools.ToolDescriptor, _ tools.ToolProvider, unavailReason string) string {
	return unavailReason
}

func resolveStaticToolStatus(cfg *config.Config, d tools.ToolDescriptor) (string, string) {
	switch d.ConfigKey {
	case "mcp.discovery.use_regex":
		return resolveDiscoveryToolSupport(cfg, cfg.Tools.MCP.Discovery.UseRegex)
	case "mcp.discovery.use_bm25":
		return resolveDiscoveryToolSupport(cfg, cfg.Tools.MCP.Discovery.UseBM25)
	}
	return "disabled", ""
}

func resolveDiscoveryToolSupport(cfg *config.Config, methodEnabled bool) (string, string) {
	if !cfg.Tools.IsToolEnabled("mcp") {
		return "disabled", ""
	}
	if !cfg.Tools.MCP.Discovery.Enabled {
		return "blocked", "requires_mcp_discovery"
	}
	if !methodEnabled {
		return "disabled", ""
	}
	return "enabled", ""
}

func applyToolState(cfg *config.Config, toolName string, enabled bool) error {
	// Look up ConfigKey from registered providers.
	for _, p := range tools.GetProviders() {
		for _, d := range p.Describe() {
			if d.Name == toolName {
				return applyConfigKey(cfg, d.ConfigKey, enabled)
			}
		}
	}
	// Check static tools.
	for _, d := range staticToolDescriptors {
		if d.Name == toolName {
			return applyConfigKey(cfg, d.ConfigKey, enabled)
		}
	}
	return fmt.Errorf("tool %q not found", toolName)
}

func applyConfigKey(cfg *config.Config, key string, enabled bool) error {
	switch key {
	case "mcp.discovery.use_regex":
		cfg.Tools.MCP.Discovery.UseRegex = enabled
		if enabled {
			cfg.Tools.MCP.Discovery.Enabled = true
		}
	case "mcp.discovery.use_bm25":
		cfg.Tools.MCP.Discovery.UseBM25 = enabled
		if enabled {
			cfg.Tools.MCP.Discovery.Enabled = true
		}
	default:
		// Generic override: global-layer tools without a dedicated typed field are
		// toggled here so the WebUI can manage them dynamically.
		if cfg.Tools.Overrides == nil {
			cfg.Tools.Overrides = map[string]bool{}
		}
		cfg.Tools.Overrides[key] = enabled
	}
	return nil
}
