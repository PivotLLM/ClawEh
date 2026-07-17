// ClawEh
// License: MIT
//
// Package mcpserver exposes claw host-side tools to MCP-compatible clients
// over a Streamable HTTP transport. It lets CLI providers (claude-cli,
// codex-cli, gemini-cli) natively call claw tools via MCP — the correct
// alternative to having the CLI emit tool-call JSON in its prose (which
// created infinite outer loops, since CLIs are themselves agentic).
//
// The server exposes two endpoints over the same registries, ACL, and token
// store, differing only in how the session token (SST<64hex>) is transported:
//
//   - /internal — every tool carries a required `session_token` parameter. This
//     is ClawEh's multi-assistant routing surface: one configured endpoint, a
//     different token per call. ClawEh's own CLI providers point here.
//   - /mcp — the standard bearer endpoint: clean tool schemas (no session_token
//     parameter) with auth via `Authorization: Bearer <token>`. A missing or
//     invalid bearer is rejected at the HTTP layer with a 401. This is the
//     universal surface for external MCP clients and the probe test suite.
//
// Either way the token resolves to an (agentID, sessionKey) pair and is the sole
// auth mechanism: it identifies both the calling agent and the active session.
// If the token is missing, malformed, unknown, or the sub-agent sentinel, the
// call fails closed.
package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/bus"
	"github.com/PivotLLM/ClawEh/pkg/global"
	"github.com/PivotLLM/ClawEh/pkg/logger"
	"github.com/PivotLLM/ClawEh/pkg/mcpserver/acl"
	"github.com/PivotLLM/ClawEh/pkg/tools"
	"github.com/mark3labs/mcp-go/server"
)

// DefaultListen is the default bind address for the MCP server.
const DefaultListen = "127.0.0.1:5911"

// DefaultEndpointPath is the default HTTP endpoint for the standard, bearer-token
// MCP surface. Tools here have clean schemas; auth is Authorization: Bearer.
const DefaultEndpointPath = "/mcp"

// InternalEndpointPath is the HTTP endpoint for ClawEh's multi-assistant
// routing: every tool carries the in-call session_token parameter. This is the
// endpoint ClawEh's own CLI providers point at.
const InternalEndpointPath = "/internal"

// MCPServer wraps the mcp-go streamable HTTP server and registers claw tools.
// Tool enumeration via tools/list is global — the catalogue published is the
// union of every agent registry, since tool names/schemas are not sensitive.
// Tool execution via tools/call dispatches through the agent-token manager:
// the token in the call body resolves to an agent identity, the per-agent
// ACL gates the (agent, tool) pair, and the per-agent registry executes.
type MCPServer struct {
	agentRegistries map[string]*tools.ToolRegistry // agentID → registry (dispatch target + schema source)
	internalAllow   []string                       // tools/list visibility filter for /internal
	externalAllow   []string                       // tools/list visibility filter for /mcp (bearer)

	// NOTE: progressive tool discovery is intentionally NOT applied on the host.
	// It is an in-loop model-context optimization only; the MCP host always
	// advertises the full allowed catalogue and never TTL-gates a dispatch, so an
	// external client gets every authorized tool and runs its own tool handling.
	listen       string
	endpointPath string // bearer endpoint (/mcp)
	internalPath string // session-token-parameter endpoint (/internal)

	sessionTokens *sessionTokenStore // SST-prefixed per-session tokens for session-scoped tools
	workspaces    map[string]string  // agentID → workspace (for boot/first-call logging)
	policy        acl.Policy         // per-agent tools/call ACL; defaults to acl.Default
	msgBus        *bus.MessageBus    // outbound publish target for tool ForUser payloads (optional)

	httpServer *http.Server
	streamable *server.StreamableHTTPServer

	// activeDispatches counts tool/call POST requests currently executing.
	// Used by Shutdown to distinguish idle SSE connections from in-flight work.
	activeDispatches atomic.Int32

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

// WithEndpointPath sets the bearer (standard) HTTP endpoint path (default:
// /mcp). The session-token-parameter endpoint is always served at /internal.
func WithEndpointPath(path string) Option {
	return func(m *MCPServer) {
		if path != "" {
			m.endpointPath = path
		}
	}
}


// WithInternalAllowlist sets the tools/list visibility filter for the /internal
// endpoint. Patterns are matched by config.MatchVisibility (equality-or-prefix,
// underscores collapsed, leading mcp_ stripped; "*" = all). An empty/nil list
// means no tools are exposed on /internal (fail-closed). Every tool obeys the
// filter; nothing is hard-excluded.
func WithInternalAllowlist(names []string) Option {
	return func(m *MCPServer) {
		if len(names) == 0 {
			m.internalAllow = nil
			return
		}
		m.internalAllow = append([]string(nil), names...)
	}
}

// WithExternalAllowlist sets the tools/list visibility filter for the bearer
// endpoint (/mcp). Same matching semantics as WithInternalAllowlist.
func WithExternalAllowlist(names []string) Option {
	return func(m *MCPServer) {
		if len(names) == 0 {
			m.externalAllow = nil
			return
		}
		m.externalAllow = append([]string(nil), names...)
	}
}

// WithAllowlist applies the same visibility filter to BOTH endpoints. Convenience
// for callers/tests that don't need per-endpoint lists.
func WithAllowlist(names []string) Option {
	return func(m *MCPServer) {
		WithInternalAllowlist(names)(m)
		WithExternalAllowlist(names)(m)
	}
}

// WithMessageBus supplies the outbound message bus. When set, MCP-routed tool
// dispatch publishes any non-Silent ForUser payload from a tool result to the
// originating user's channel/chatID (looked up from the session record). When
// nil or unset, ForUser content from MCP-routed tool calls is silently dropped
// with a log line; the MCP response envelope is unaffected.
func WithMessageBus(b *bus.MessageBus) Option {
	return func(m *MCPServer) { m.msgBus = b }
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
		listen:        DefaultListen,
		endpointPath:  DefaultEndpointPath,
		internalPath:  InternalEndpointPath,
		sessionTokens: newSessionTokenStore(),
	}
	for _, opt := range opts {
		opt(m)
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

	newSrv := func() *server.MCPServer {
		return server.NewMCPServer(global.AppName, global.Version,
			server.WithToolCapabilities(true),
			server.WithRecovery(),
		)
	}

	// /internal — session-token parameter on every tool (ClawEh's CLI providers).
	internalSrv := newSrv()
	addToolsToServer(internalSrv, internalAuthMode, m.agentRegistries, m.internalAllow, m.sessionTokens, resolver, tracker, m.policy, m.msgBus, &m.activeDispatches)

	// /mcp — standard bearer endpoint, clean tool schemas (probe / external MCP).
	bearerSrv := newSrv()
	addToolsToServer(bearerSrv, bearerAuthMode, m.agentRegistries, m.externalAllow, m.sessionTokens, resolver, tracker, m.policy, m.msgBus, &m.activeDispatches)

	internalStreamable := newStreamable(internalSrv, m.internalPath, false)
	bearerStreamable := newStreamable(bearerSrv, m.endpointPath, true)

	// srv/streamable keep the internal server for test introspection (its schemas
	// carry the session_token param, matching the historical single-endpoint).
	m.srv = internalSrv
	m.streamable = internalStreamable

	mux := http.NewServeMux()
	mux.Handle(m.internalPath, internalStreamable)
	mux.Handle(m.endpointPath, bearerAuthMiddleware(m.sessionTokens, bearerStreamable))
	m.httpServer = &http.Server{Handler: mux}

	return m, nil
}

// Listen returns the configured listen address.
func (m *MCPServer) Listen() string { return m.listen }

// EndpointPath returns the configured endpoint path.
func (m *MCPServer) EndpointPath() string { return m.endpointPath }

// SessionTokens returns the session token store. Callers (e.g. the AgentLoop)
// use it to issue tokens when a new session context manager is created and to
// revoke tokens on session clear or eviction.
func (m *MCPServer) SessionTokens() *sessionTokenStore { return m.sessionTokens }

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
			"endpoint": m.endpointPath, // bearer (standard)
			"internal": m.internalPath, // session-token parameter
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

// Shutdown stops the MCP server. It waits briefly for any in-flight tool
// dispatches (POST requests) to complete, then force-closes all remaining
// connections — including the persistent SSE GET connections that CLI clients
// hold open indefinitely. Without the force-close step, http.Server.Shutdown
// blocks for the full context deadline (typically 30 s) waiting for those
// idle SSE connections to drain on their own.
func (m *MCPServer) Shutdown(ctx context.Context) error {
	logger.InfoCF("mcpserver", "MCP server shutting down", nil)

	if m.httpServer != nil {
		// Stop accepting new connections immediately.
		_ = m.httpServer.Close()
	}

	// Wait up to 3 s for in-flight tool dispatches to finish.
	deadline := time.Now().Add(3 * time.Second)
	for m.activeDispatches.Load() > 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}

	if remaining := m.activeDispatches.Load(); remaining > 0 {
		logger.WarnCF("mcpserver", "Shutdown with active dispatches still in flight",
			map[string]any{"count": remaining})
	}

	return nil
}
