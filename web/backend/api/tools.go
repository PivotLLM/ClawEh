package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"

	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/tools"
)

// staticToolDescriptors holds descriptors for tools not owned by any ToolProvider
// (discovery tools registered by the MCP layer).
var staticToolDescriptors = []tools.ToolDescriptor{
	{Name: "find_tools_regex", Description: "Discover hidden MCP tools by regex search when tool discovery is enabled.", Category: "discovery", ConfigKey: "mcp.discovery.use_regex", DefaultEnabled: false},
	{Name: "find_tools_bm25", Description: "Discover hidden MCP tools by semantic ranking when tool discovery is enabled.", Category: "discovery", ConfigKey: "mcp.discovery.use_bm25", DefaultEnabled: false},
}

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

func resolveToolStatus(cfg *config.Config, d tools.ToolDescriptor, p tools.ToolProvider, unavailReason string) string {
	if unavailReason != "" {
		return "blocked"
	}
	switch d.Name {
	case "skills_find", "skills_install":
		if !cfg.Tools.IsToolEnabled(d.ConfigKey) {
			return "disabled"
		}
		if !cfg.Tools.IsToolEnabled("skills") {
			return "blocked"
		}
		return "enabled"
	case "agents_spawn":
		if !cfg.Tools.IsToolEnabled(d.ConfigKey) {
			return "disabled"
		}
		if !cfg.Tools.IsToolEnabled("subagent") {
			return "blocked"
		}
		return "enabled"
	case "hw_i2c", "hw_spi":
		if !cfg.Tools.IsToolEnabled(d.ConfigKey) {
			return "disabled"
		}
		if runtime.GOOS != "linux" {
			return "blocked"
		}
		return "enabled"
	case "session_messages", "session_search", "session_compact", "session_info":
		return "enabled"
	default:
		if cfg.Tools.IsToolEnabled(d.ConfigKey) {
			return "enabled"
		}
		return "disabled"
	}
}

func resolveToolReasonCode(cfg *config.Config, d tools.ToolDescriptor, p tools.ToolProvider, unavailReason string) string {
	if unavailReason != "" {
		return unavailReason
	}
	switch d.Name {
	case "skills_find", "skills_install":
		if cfg.Tools.IsToolEnabled(d.ConfigKey) && !cfg.Tools.IsToolEnabled("skills") {
			return "requires_skills"
		}
	case "agents_spawn":
		if cfg.Tools.IsToolEnabled(d.ConfigKey) && !cfg.Tools.IsToolEnabled("subagent") {
			return "requires_subagent"
		}
	case "hw_i2c", "hw_spi":
		if cfg.Tools.IsToolEnabled(d.ConfigKey) && runtime.GOOS != "linux" {
			return "requires_linux"
		}
	}
	return ""
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
	case "files_read":
		cfg.Tools.ReadFile.Enabled = enabled
	case "files_write":
		cfg.Tools.WriteFile.Enabled = enabled
	case "files_list":
		cfg.Tools.ListDir.Enabled = enabled
	case "files_edit":
		cfg.Tools.EditFile.Enabled = enabled
	case "files_append":
		cfg.Tools.AppendFile.Enabled = enabled
	case "files_copy":
		cfg.Tools.CopyFile.Enabled = enabled
	case "shell_exec":
		cfg.Tools.Exec.Enabled = enabled
	case "schedule_cron":
		cfg.Tools.Cron.Enabled = enabled
	case "web":
		cfg.Tools.Web.Enabled = enabled
	case "web_fetch":
		cfg.Tools.WebFetch.Enabled = enabled
	case "msg_send":
		cfg.Tools.Message.Enabled = enabled
	case "msg_send_file":
		cfg.Tools.SendFile.Enabled = enabled
	case "skills_find":
		cfg.Tools.FindSkills.Enabled = enabled
		if enabled {
			cfg.Tools.Skills.Registry.Enabled = true
		}
	case "skills_install":
		cfg.Tools.InstallSkill.Enabled = enabled
		if enabled {
			cfg.Tools.Skills.Registry.Enabled = true
		}
	case "agents_spawn":
		cfg.Tools.Spawn.Enabled = enabled
		if enabled {
			cfg.Tools.Subagent.Enabled = true
		}
	case "hw_i2c":
		cfg.Tools.I2C.Enabled = enabled
	case "hw_spi":
		cfg.Tools.SPI.Enabled = enabled
	case "mcp.discovery.use_regex":
		cfg.Tools.MCP.Discovery.UseRegex = enabled
		if enabled {
			cfg.Tools.MCP.Enabled = true
			cfg.Tools.MCP.Discovery.Enabled = true
		}
	case "mcp.discovery.use_bm25":
		cfg.Tools.MCP.Discovery.UseBM25 = enabled
		if enabled {
			cfg.Tools.MCP.Enabled = true
			cfg.Tools.MCP.Discovery.Enabled = true
		}
	default:
		return fmt.Errorf("config key %q not mapped", key)
	}
	return nil
}
