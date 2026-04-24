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
	registry      *tools.ToolRegistry
	allowPatterns []string
	listen        string
	endpointPath  string

	srv        *server.MCPServer
	httpServer *server.StreamableHTTPServer
}

// Option configures an MCPServer.
type Option func(*MCPServer)

// WithRegistry sets the tool registry that the MCP server exposes.
func WithRegistry(r *tools.ToolRegistry) Option {
	return func(m *MCPServer) { m.registry = r }
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

	m.srv = server.NewMCPServer(global.AppName, global.Version,
		server.WithToolCapabilities(false),
		server.WithRecovery(),
	)

	m.addTools()

	m.httpServer = server.NewStreamableHTTPServer(m.srv,
		server.WithEndpointPath(m.endpointPath),
		server.WithStateLess(true),
	)

	return m, nil
}

// Listen returns the configured listen address.
func (m *MCPServer) Listen() string { return m.listen }

// EndpointPath returns the configured endpoint path.
func (m *MCPServer) EndpointPath() string { return m.endpointPath }

// Start begins serving in a background goroutine. It returns after the
// listener is bound (or immediately on bind failure), so callers can rely
// on the server being ready once Start returns nil.
func (m *MCPServer) Start() error {
	ln, err := net.Listen("tcp", m.listen)
	if err != nil {
		return fmt.Errorf("mcpserver: bind %s: %w", m.listen, err)
	}
	// Close the probe listener; the real server binds its own.
	_ = ln.Close()

	logger.InfoCF("mcpserver", "MCP server starting",
		map[string]any{"listen": m.listen, "endpoint": m.endpointPath})

	errCh := make(chan error, 1)
	go func() {
		if err := m.httpServer.Start(m.listen); err != nil {
			logger.ErrorCF("mcpserver", "MCP server exited",
				map[string]any{"error": err.Error()})
			errCh <- err
			return
		}
		errCh <- nil
	}()

	// Give the server a brief window to fail fast (e.g., port already bound
	// by a parallel process).
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
	return m.httpServer.Shutdown(ctx)
}
