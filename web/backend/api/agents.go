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
	Tools      []agentToolEntry `json:"tools"`
	MCPServers []agentMCPServer `json:"mcp_servers,omitempty"`
}

func (h *Handler) registerAgentRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/agents/tools", h.handleListAgentTools)
}

func (h *Handler) handleListAgentTools(w http.ResponseWriter, r *http.Request) {
	// Flat list of built-in tools in display order
	builtinTools := make([]agentToolEntry, 0, len(toolCatalog))
	for _, entry := range toolCatalog {
		builtinTools = append(builtinTools, agentToolEntry{
			Name:        entry.Name,
			Description: entry.Description,
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

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(agentToolCatalogResponse{
		Tools:      builtinTools,
		MCPServers: mcpServers,
	})
}
