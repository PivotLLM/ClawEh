// ClawEh
// License: MIT

package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/PivotLLM/ClawEh/mcp"
)

// mcpStatusLoop is the slice of the running AgentLoop the MCP status endpoint
// needs. Injected via SetMCPStatusLoop so this package stays decoupled from
// agent and always reads the live manager (valid across config reloads,
// which reuse the manager in place).
type mcpStatusLoop interface {
	MCPStatus() []mcp.ServerStatus
	// RefreshMCPServer disconnects and reconnects one server, re-registering
	// its tools; mcp.ErrUnknownServer when the name is not a configured server.
	RefreshMCPServer(ctx context.Context, name string) error
}

// SetMCPStatusLoop wires the live AgentLoop into the handler so the status
// endpoint reports the gateway's real outbound-MCP connection state.
func (h *Handler) SetMCPStatusLoop(loop mcpStatusLoop) {
	h.reloadMu.Lock()
	h.mcpStatusLoop = loop
	h.reloadMu.Unlock()
}

func (h *Handler) mcpStatusLoopRef() mcpStatusLoop {
	h.reloadMu.Lock()
	defer h.reloadMu.Unlock()
	return h.mcpStatusLoop
}

func (h *Handler) registerMCPStatusRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/mcp/status", h.handleMCPStatus)
	mux.HandleFunc("POST /api/mcp/servers/{name}/reconnect", h.handleMCPReconnect)
}

// mcpStatusResponse is the shape the WebUI MCP page polls. It carries only the
// live states the manager tracks (connected / reconnecting / cooldown); the
// frontend maps these onto its configured server list, treating any server not
// present here as disconnected (or disabled, per its config toggle).
type mcpStatusResponse struct {
	Servers []mcp.ServerStatus `json:"servers"`
}

// handleMCPStatus reports the live connection state of outbound MCP servers.
//
//	GET /api/mcp/status
func (h *Handler) handleMCPStatus(w http.ResponseWriter, r *http.Request) {
	loop := h.mcpStatusLoopRef()
	var servers []mcp.ServerStatus
	if loop != nil {
		servers = loop.MCPStatus()
	}
	if servers == nil {
		servers = []mcp.ServerStatus{}
	}
	writeJSON(w, http.StatusOK, mcpStatusResponse{Servers: servers})
}

// handleMCPReconnect forces one outbound MCP server to disconnect, reconnect
// and re-register its tools, so a server restarted with a changed tool list
// is picked up without a gateway restart.
//
//	POST /api/mcp/servers/{name}/reconnect
func (h *Handler) handleMCPReconnect(w http.ResponseWriter, r *http.Request) {
	loop := h.mcpStatusLoopRef()
	if loop == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "gateway not running"})
		return
	}
	name := r.PathValue("name")
	if err := loop.RefreshMCPServer(r.Context(), name); err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, mcp.ErrUnknownServer) {
			status = http.StatusNotFound
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "server": name})
}
