// ClawEh
// License: MIT
//
// Package mcpserver exposes claw host-side tools to MCP-compatible clients
// over a Streamable HTTP transport. It lets CLI providers (claude-cli,
// codex-cli, gemini-cli) natively call claw tools via MCP — the correct
// alternative to having the CLI emit tool-call JSON in its prose (which
// created infinite outer loops, since CLIs are themselves agentic).
package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/global"
	"github.com/PivotLLM/ClawEh/pkg/logger"
	"github.com/PivotLLM/ClawEh/pkg/tools"
	"github.com/mark3labs/mcp-go/server"
)

// DefaultListen is the default bind address for the MCP server.
const DefaultListen = "127.0.0.1:5911"

// DefaultEndpointPath is the default HTTP endpoint for MCP traffic.
const DefaultEndpointPath = "/mcp"

// MCPServer wraps the mcp-go streamable HTTP server and registers claw tools.
type MCPServer struct {
	registry        *tools.ToolRegistry            // default
	agentRegistries map[string]*tools.ToolRegistry // agentID → registry
	allowPatterns   []string
	listen          string
	endpointPath    string // base path, e.g. "/mcp"

	// internal
	httpServer *http.Server
	agentSrvs  map[string]*server.StreamableHTTPServer // agentID → server
	defaultSrv *server.StreamableHTTPServer

	// srv is kept for test introspection of the default endpoint's MCPServer.
	srv *server.MCPServer
}

// Option configures an MCPServer.
type Option func(*MCPServer)

// WithRegistry sets the tool registry that the MCP server exposes.
func WithRegistry(r *tools.ToolRegistry) Option {
	return func(m *MCPServer) { m.registry = r }
}

// WithAgentRegistries sets per-agent tool registries.
// Each agent is served at endpointPath+"/"+agentID (e.g., /mcp/karen).
func WithAgentRegistries(registries map[string]*tools.ToolRegistry) Option {
	return func(m *MCPServer) {
		if len(registries) > 0 {
			m.agentRegistries = make(map[string]*tools.ToolRegistry, len(registries))
			for k, v := range registries {
				m.agentRegistries[k] = v
			}
		}
	}
}

// WithListen sets the listen address (default: 127.0.0.1:5911).
func WithListen(addr string) Option {
	return func(m *MCPServer) {
		if addr != "" {
			m.listen = addr
		}
	}
}

// WithEndpointPath sets the HTTP endpoint path (default: /mcp).
func WithEndpointPath(path string) Option {
	return func(m *MCPServer) {
		if path != "" {
			m.endpointPath = path
		}
	}
}

// WithAllowlist sets the tool-name patterns to expose. Supports "*" (all),
// prefix globs like "read_*", and exact names — see config.MatchToolPattern.
// An empty or nil allowlist means no tools are exposed (fail-closed). The
// "message" tool is never exposed regardless of the allowlist — it belongs
// to the agent's outbound-publish path, not MCP clients.
func WithAllowlist(names []string) Option {
	return func(m *MCPServer) {
		if len(names) == 0 {
			m.allowPatterns = nil
			return
		}
		m.allowPatterns = append([]string(nil), names...)
	}
}

// buildEndpoint creates a StreamableHTTPServer for a given registry and path.
func buildEndpoint(registry *tools.ToolRegistry, allowPatterns []string, endpointPath string) (*server.MCPServer, *server.StreamableHTTPServer) {
	mcpSrv := server.NewMCPServer(global.AppName, global.Version,
		server.WithToolCapabilities(false),
		server.WithRecovery(),
	)
	addToolsToServer(mcpSrv, registry, allowPatterns)
	httpSrv := server.NewStreamableHTTPServer(mcpSrv,
		server.WithEndpointPath(endpointPath),
		server.WithStateLess(true),
	)
	return mcpSrv, httpSrv
}

// New constructs an MCPServer. The registry option is required.
func New(opts ...Option) (*MCPServer, error) {
	m := &MCPServer{
		listen:       DefaultListen,
		endpointPath: DefaultEndpointPath,
	}
	for _, opt := range opts {
		opt(m)
	}

	if m.registry == nil {
		return nil, errors.New("mcpserver: registry is required")
	}

	mux := http.NewServeMux()

	// Default endpoint
	m.srv, m.defaultSrv = buildEndpoint(m.registry, m.allowPatterns, m.endpointPath)
	mux.Handle(m.endpointPath, m.defaultSrv)

	// Per-agent endpoints
	m.agentSrvs = make(map[string]*server.StreamableHTTPServer)
	for agentID, reg := range m.agentRegistries {
		if strings.Contains(agentID, "/") {
			logger.WarnCF("mcpserver", "skipping agent endpoint: ID contains '/'",
				map[string]any{"agent_id": agentID})
			continue
		}
		agentPath := m.endpointPath + "/" + agentID
		_, agentSrv := buildEndpoint(reg, m.allowPatterns, agentPath)
		m.agentSrvs[agentID] = agentSrv
		mux.Handle(agentPath, agentSrv)
	}

	m.httpServer = &http.Server{Handler: mux}

	return m, nil
}

// Listen returns the configured listen address.
func (m *MCPServer) Listen() string { return m.listen }

// EndpointPath returns the configured endpoint path.
func (m *MCPServer) EndpointPath() string { return m.endpointPath }

// Start begins serving in a background goroutine. It returns after the
// listener is bound (binding failures are returned immediately), so callers
// can rely on the server being ready once Start returns nil.
func (m *MCPServer) Start() error {
	ln, err := net.Listen("tcp", m.listen)
	if err != nil {
		return fmt.Errorf("mcpserver: bind %s: %w", m.listen, err)
	}

	logger.InfoCF("mcpserver", "MCP server starting",
		map[string]any{
			"listen":          m.listen,
			"endpoint":        m.endpointPath,
			"agent_endpoints": len(m.agentSrvs),
		})

	// Use Serve(ln) rather than ListenAndServe so the port is held from the
	// moment Start returns — no TOCTOU gap between binding and serving.
	errCh := make(chan error, 1)
	go func() {
		if err := m.httpServer.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.ErrorCF("mcpserver", "MCP server exited",
				map[string]any{"error": err.Error()})
			errCh <- err
			return
		}
		errCh <- nil
	}()

	// Give the server a brief window to fail fast.
	select {
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("mcpserver: start failed: %w", err)
		}
		return nil
	case <-time.After(150 * time.Millisecond):
		return nil
	}
}

// Shutdown gracefully stops the HTTP server.
func (m *MCPServer) Shutdown(ctx context.Context) error {
	logger.InfoCF("mcpserver", "MCP server shutting down", nil)
	// Shutdown per-agent servers (best-effort; they don't own the http.Server).
	for _, srv := range m.agentSrvs {
		_ = srv.Shutdown(ctx)
	}
	if m.defaultSrv != nil {
		_ = m.defaultSrv.Shutdown(ctx)
	}
	if m.httpServer != nil {
		return m.httpServer.Shutdown(ctx)
	}
	return nil
}

// WriteWorkspaceConfigs writes per-agent .claude.json files mapping each agent's
// workspace to its MCP endpoint path. baseURL is the server's base URL (e.g.
// "http://127.0.0.1:5911"). workspaces maps agentID → workspace path.
func (m *MCPServer) WriteWorkspaceConfigs(baseURL string, workspaces map[string]string) {
	for agentID := range m.agentRegistries {
		workspace, ok := workspaces[agentID]
		if !ok || workspace == "" {
			continue
		}
		agentURL := baseURL + m.endpointPath + "/" + agentID
		if err := WriteAgentWorkspaceConfig(workspace, agentURL); err != nil {
			logger.WarnCF("mcpserver", "Failed to write workspace MCP config",
				map[string]any{"agent": agentID, "workspace": workspace, "error": err.Error()})
		} else {
			logger.DebugCF("mcpserver", "Wrote workspace MCP config",
				map[string]any{"agent": agentID, "path": workspace + "/.claude.json"})
		}
	}
}
