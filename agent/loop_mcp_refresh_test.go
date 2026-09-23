// ClawEh
// License: MIT

package agent

import (
	"context"
	"errors"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
	clawmcp "github.com/PivotLLM/ClawEh/mcp"
)

// newRefreshTestServer serves an in-process streamable-HTTP MCP server with one
// tool, "ping", and returns the handle for changing its tool list.
func newRefreshTestServer(t *testing.T) (*server.MCPServer, *httptest.Server) {
	t.Helper()
	srv := server.NewMCPServer("test", "0.0.1")
	srv.AddTool(mcp.NewTool("ping", mcp.WithDescription("no-op")),
		func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{}, nil
		})
	ts := httptest.NewServer(server.NewStreamableHTTPServer(srv))
	t.Cleanup(ts.Close)
	return srv, ts
}

// newMCPAgentLoop builds a single-agent loop whose agent is allowed every tool
// of the external MCP server "svc", connected and registered.
func newMCPAgentLoop(t *testing.T, url string) *AgentLoop {
	t.Helper()
	cfg := toolRegTestConfig(t)
	cfg.Agents.List[0].MCPTools = []string{"svc"}
	cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{
		"svc": {Enabled: true, Type: "http", URL: url},
	}
	al := mustNewAgentLoop(t, cfg, bus.NewMessageBus(), &mockProvider{}, nil)
	t.Cleanup(al.Close)
	if err := al.EnsureMCPInitialized(context.Background()); err != nil {
		t.Fatalf("EnsureMCPInitialized: %v", err)
	}
	return al
}

// catalogueCounter records how often the host was asked to refresh.
type catalogueCounter struct{ refreshes int }

func (c *catalogueCounter) RefreshCatalogue() { c.refreshes++ }

// After the tools-changed handler runs (here driven through RefreshMCPServer,
// which reconnects and re-registers), the old mcp_svc_ping name is gone from the
// agent registry, the new mcp_svc_pong is present, and the host was refreshed.
func TestRefreshMCPServer_ReplacesRenamedTools(t *testing.T) {
	srv, ts := newRefreshTestServer(t)
	al := newMCPAgentLoop(t, ts.URL)
	host := &catalogueCounter{}
	al.SetMCPHost(host)

	names := agentToolNames(t, al)
	assertHasTool(t, names, "mcp_svc_ping")

	srv.DeleteTools("ping")
	srv.AddTool(mcp.NewTool("pong", mcp.WithDescription("no-op")),
		func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{}, nil
		})

	if err := al.RefreshMCPServer(context.Background(), "svc"); err != nil {
		t.Fatalf("RefreshMCPServer: %v", err)
	}

	names = agentToolNames(t, al)
	if slices.Contains(names, "mcp_svc_ping") {
		t.Errorf("renamed tool mcp_svc_ping should be gone; got %v", names)
	}
	assertHasTool(t, names, "mcp_svc_pong")
	if host.refreshes == 0 {
		t.Error("the MCP host catalogue should have been refreshed")
	}
}

func TestRefreshMCPServer_UnknownServer(t *testing.T) {
	_, ts := newRefreshTestServer(t)
	al := newMCPAgentLoop(t, ts.URL)

	if err := al.RefreshMCPServer(context.Background(), "nope"); !errors.Is(err, clawmcp.ErrUnknownServer) {
		t.Fatalf("RefreshMCPServer(unknown) = %v, want ErrUnknownServer", err)
	}
}

// With no MCP manager at all (MCP not configured) every name is unknown.
func TestRefreshMCPServer_NoManager(t *testing.T) {
	al := mustNewAgentLoop(t, toolRegTestConfig(t), bus.NewMessageBus(), &mockProvider{}, nil)
	t.Cleanup(al.Close)

	if err := al.RefreshMCPServer(context.Background(), "svc"); !errors.Is(err, clawmcp.ErrUnknownServer) {
		t.Fatalf("RefreshMCPServer without a manager = %v, want ErrUnknownServer", err)
	}
}
