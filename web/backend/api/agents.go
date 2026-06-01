package api

import (
	"encoding/json"
	"net/http"
	"sort"

	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/tools"
)

type agentToolEntry struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type agentMCPServer struct {
	Name    string `json:"name"`
	Pattern string `json:"pattern"`
}

type agentToolCatalogResponse struct {
	Tools        []agentToolEntry `json:"tools"`
	MCPServers   []agentMCPServer `json:"mcp_servers,omitempty"`
	DefaultTools []string         `json:"default_tools"`
}

func (h *Handler) registerAgentRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/agents/tools", h.handleListAgentTools)
}

func (h *Handler) handleListAgentTools(w http.ResponseWriter, r *http.Request) {
	// Flat list of built-in tools in display order, built dynamically from providers.
	var builtinTools []agentToolEntry
	for _, p := range tools.GetProviders() {
		for _, d := range p.Describe() {
			builtinTools = append(builtinTools, agentToolEntry{
				Name:        d.Name,
				Description: d.Description,
			})
		}
	}
	// Append static (non-provider) tools.
	for _, d := range staticToolDescriptors {
		builtinTools = append(builtinTools, agentToolEntry{
			Name:        d.Name,
			Description: d.Description,
		})
	}

	// MCP servers: one entry per configured server; selecting it grants mcp_name_* access
	var mcpServers []agentMCPServer
	if cfg, err := config.LoadConfig(h.configPath); err == nil && cfg.Tools.MCP.Enabled {
		for name := range cfg.Tools.MCP.Servers {
			mcpServers = append(mcpServers, agentMCPServer{
				Name:    name,
				Pattern: tools.MCPServerPattern(name),
			})
		}
		sort.Slice(mcpServers, func(i, j int) bool {
			return mcpServers[i].Name < mcpServers[j].Name
		})
	}

	effectiveDefaults := config.DefaultAgentTools
	if cfg, cfgErr := config.LoadConfig(h.configPath); cfgErr == nil {
		if len(cfg.Agents.Defaults.DefaultTools) > 0 {
			effectiveDefaults = cfg.Agents.Defaults.DefaultTools
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(agentToolCatalogResponse{
		Tools:        builtinTools,
		MCPServers:   mcpServers,
		DefaultTools: effectiveDefaults,
	})
}
