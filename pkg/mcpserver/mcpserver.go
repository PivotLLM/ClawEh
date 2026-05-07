// ClawEh
// License: MIT
//
// Package mcpserver exposes claw host-side tools to MCP-compatible clients
// over a Streamable HTTP transport. It lets CLI providers (claude-cli,
// codex-cli, gemini-cli) natively call claw tools via MCP — the correct
// alternative to having the CLI emit tool-call JSON in its prose (which
// created infinite outer loops, since CLIs are themselves agentic).
//
// Tool calls carry an `agent_token` parameter (snake_case) which the
// server resolves against the agent-token manager to root path resolution
// at the calling agent's own workspace. There is no fallback to a shared
// root: if the token is missing, malformed, unknown, or the sub-agent
// sentinel, the call fails closed with a clear error.
package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/agenttoken"
	"github.com/PivotLLM/ClawEh/pkg/global"
	"github.com/PivotLLM/ClawEh/pkg/logger"
	"github.com/PivotLLM/ClawEh/pkg/mcpserver/acl"
	"github.com/PivotLLM/ClawEh/pkg/tools"
	"github.com/mark3labs/mcp-go/server"
)

// DefaultListen is the default bind address for the MCP server.
const DefaultListen = "127.0.0.1:5911"

// DefaultEndpointPath is the default HTTP endpoint for MCP traffic.
const DefaultEndpointPath = "/mcp"

// MCPServer wraps the mcp-go streamable HTTP server and registers claw tools.
// Tool enumeration via tools/list is global — the catalogue published is the
// union of every agent registry, since tool names/schemas are not sensitive.
// Tool execution via tools/call dispatches through the agent-token manager:
// the token in the call body resolves to an agent identity, the per-agent
// ACL gates the (agent, tool) pair, and the per-agent registry executes.
type MCPServer struct {
	agentRegistries map[string]*tools.ToolRegistry // agentID → registry (dispatch target + schema source)
	allowPatterns   []string
	listen          string
	endpointPath    string

	tokens     *agenttoken.Manager
	workspaces map[string]string // agentID → workspace (for boot/first-call logging)
	policy     acl.Policy        // per-agent tools/call ACL; defaults to acl.Default

	httpServer *http.Server
	streamable *server.StreamableHTTPServer

	// srv is kept for test introspection.
	srv *server.MCPServer
}

// Option configures an MCPServer.
type Option func(*MCPServer)

// WithAgentRegistries sets the per-agent tool registries. The same map
// drives both the global tools/list catalogue (union of all registries,
// deduped by name) and the tools/call dispatch target (keyed by the
// agent ID that the token manager resolves to).
func WithAgentRegistries(registries map[string]*tools.ToolRegistry) Option {
	return func(m *MCPServer) {
		if len(registries) == 0 {
			m.agentRegistries = nil
			return
		}
		m.agentRegistries = make(map[string]*tools.ToolRegistry, len(registries))
		for k, v := range registries {
			m.agentRegistries[k] = v
		}
	}
}

// WithAgentTokens supplies the token manager that resolves the
// `agent_token` call parameter to an agent ID. Required.
func WithAgentTokens(t *agenttoken.Manager) Option {
	return func(m *MCPServer) { m.tokens = t }
}

// WithAgentWorkspaces supplies the agentID → workspace map. Used only
// for boot-log assertions and the first-MCP-call-per-agent log entry.
func WithAgentWorkspaces(ws map[string]string) Option {
	return func(m *MCPServer) {
		if len(ws) == 0 {
			m.workspaces = nil
			return
		}
		m.workspaces = make(map[string]string, len(ws))
		for k, v := range ws {
			m.workspaces[k] = v
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

// WithACLPolicy installs a per-agent ACL policy consulted on every
// tools/call after token validation. tools/list is never gated by this.
// A nil argument is treated as acl.Default (open by default).
func WithACLPolicy(p acl.Policy) Option {
	return func(m *MCPServer) { m.policy = p }
}

// New constructs an MCPServer. An agent-token manager and at least one
// agent registry are required; the agent registries double as the schema
// source for tools/list (union of all of them, deduped by name).
func New(opts ...Option) (*MCPServer, error) {
	m := &MCPServer{
		listen:       DefaultListen,
		endpointPath: DefaultEndpointPath,
	}
	for _, opt := range opts {
		opt(m)
	}

	if m.tokens == nil {
		return nil, errors.New("mcpserver: agent-token manager is required")
	}
	if len(m.agentRegistries) == 0 {
		return nil, errors.New("mcpserver: at least one agent registry is required")
	}
	if m.policy == nil {
		m.policy = acl.Default
	}

	// Boot-log assertion: emit one line per registered agent so any
	// mis-bindings are visible at startup.
	for agentID, reg := range m.agentRegistries {
		ws := m.workspaces[agentID]
		logger.InfoCF("mcpserver", "Agent registry bound",
			map[string]any{
				"agent":     agentID,
				"workspace": ws,
				"tools":     reg.Count(),
			})
	}

	tracker := newFirstCallTracker(m.workspaces)

	resolver := func(agentName string) (*tools.ToolRegistry, bool) {
		reg, ok := m.agentRegistries[agentName]
		return reg, ok
	}

	mcpSrv := server.NewMCPServer(global.AppName, global.Version,
		server.WithToolCapabilities(false),
		server.WithRecovery(),
	)
	addToolsToServer(mcpSrv, m.agentRegistries, m.allowPatterns, m.tokens, resolver, tracker, m.policy)

	httpSrv := server.NewStreamableHTTPServer(mcpSrv,
		server.WithEndpointPath(m.endpointPath),
		server.WithStateLess(true),
	)
	m.srv = mcpSrv
	m.streamable = httpSrv

	mux := http.NewServeMux()
	mux.Handle(m.endpointPath, httpSrv)
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
			"listen":   m.listen,
			"endpoint": m.endpointPath,
			"agents":   len(m.agentRegistries),
		})

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
	if m.streamable != nil {
		_ = m.streamable.Shutdown(ctx)
	}
	if m.httpServer != nil {
		return m.httpServer.Shutdown(ctx)
	}
	return nil
}

// WriteWorkspaceConfigs writes per-agent .claude.json files mapping each
// agent's workspace to the (single) MCP endpoint URL. With token-based
// isolation the URL is the same for every agent — routing happens in the
// call body via `agent_token`. baseURL is the server's base URL (e.g.
// "http://127.0.0.1:5911"). workspaces maps agentID → workspace path.
func (m *MCPServer) WriteWorkspaceConfigs(baseURL string, workspaces map[string]string) {
	url := baseURL + m.endpointPath
	for agentID, workspace := range workspaces {
		if workspace == "" {
			continue
		}
		if err := WriteAgentWorkspaceConfig(workspace, url); err != nil {
			logger.WarnCF("mcpserver", "Failed to write workspace MCP config",
				map[string]any{"agent": agentID, "workspace": workspace, "error": err.Error()})
		} else {
			logger.DebugCF("mcpserver", "Wrote workspace MCP config",
				map[string]any{"agent": agentID, "path": workspace + "/.claude.json"})
		}
	}
}
