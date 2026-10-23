// ClawEh
// License: MIT

package agent

import (
	"context"
	"errors"
	"net/http/httptest"
	"slices"
	"sync/atomic"
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
	t.Cleanup(func() { al.Close(context.Background()) })
	if err := al.EnsureMCPInitialized(context.Background()); err != nil {
		t.Fatalf("EnsureMCPInitialized: %v", err)
	}
	return al
}

// catalogueCounter records how often the host was asked to refresh. The
// tools-changed notifier refreshes from its own goroutine, so it is atomic.
type catalogueCounter struct{ refreshes atomic.Int32 }

func (c *catalogueCounter) RefreshCatalogue() { c.refreshes.Add(1) }

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
	if host.refreshes.Load() == 0 {
		t.Error("the MCP host catalogue should have been refreshed")
	}
}

// A refresh swaps a server's tools in one step: a turn reading the agent's
// tools while the server is re-registered (the tools-changed handler, which
// can run more than once after a reconnect) always finds them. Removing the
// old set and registering the new one as two steps left a window in which
// mcp_svc_ping was missing.
func TestRefreshMCPServerTools_ToolNeverMissing(t *testing.T) {
	_, ts := newRefreshTestServer(t)
	al := newMCPAgentLoop(t, ts.URL)
	mgr := al.mcp.peekManager()
	agent, ok := al.GetRegistry().Get("main")
	if !ok {
		t.Fatal("agent main missing")
	}

	const refreshes = 200
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range refreshes {
			al.refreshMCPServerTools(mgr, "svc")
		}
	}()
	for reads := 0; ; reads++ {
		select {
		case <-done:
			if _, ok := agent.Tools.Get("mcp_svc_ping"); !ok {
				t.Fatal("mcp_svc_ping missing after the refreshes")
			}
			return
		default:
		}
		if _, ok := agent.Tools.Get("mcp_svc_ping"); !ok {
			<-done
			t.Fatalf("mcp_svc_ping missing during a refresh (read %d)", reads)
		}
	}
}

// A server that is gone takes only its own tools with it: another server whose
// name extends its name ("svc" and "svc_docs") shares the tool-name prefix
// mcp_svc_, and keeps its tools.
func TestRefreshMCPServerTools_GoneServerKeepsLongerNamedServer(t *testing.T) {
	_, svc := newRefreshTestServer(t)
	docsSrv := server.NewMCPServer("docs", "0.0.1")
	docsSrv.AddTool(mcp.NewTool("page", mcp.WithDescription("no-op")),
		func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{}, nil
		})
	docs := httptest.NewServer(server.NewStreamableHTTPServer(docsSrv))
	t.Cleanup(docs.Close)

	cfg := toolRegTestConfig(t)
	cfg.Agents.List[0].MCPTools = []string{"svc", "svc_docs"}
	cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{
		"svc":      {Enabled: true, Type: "http", URL: svc.URL},
		"svc_docs": {Enabled: true, Type: "http", URL: docs.URL},
	}
	al := mustNewAgentLoop(t, cfg, bus.NewMessageBus(), &mockProvider{}, nil)
	t.Cleanup(func() { al.Close(context.Background()) })
	if err := al.EnsureMCPInitialized(context.Background()); err != nil {
		t.Fatalf("EnsureMCPInitialized: %v", err)
	}
	names := agentToolNames(t, al)
	assertHasTool(t, names, "mcp_svc_ping")
	assertHasTool(t, names, "mcp_svc_docs_page")

	// svc goes away: its reconnect fails and the manager drops it. The
	// client's open notification stream would hold Close until it ends, so
	// the connections are cut first.
	svc.CloseClientConnections()
	svc.Close()
	mgr := al.mcp.peekManager()
	if err := mgr.Reconnect(context.Background(), "svc"); err == nil {
		t.Fatal("Reconnect to a closed server succeeded")
	}
	if _, ok := mgr.GetServer("svc"); ok {
		t.Fatal("svc still connected after a failed reconnect")
	}
	al.refreshMCPServerTools(mgr, "svc")

	names = agentToolNames(t, al)
	if slices.Contains(names, "mcp_svc_ping") {
		t.Errorf("mcp_svc_ping should be gone with its server; got %v", names)
	}
	assertHasTool(t, names, "mcp_svc_docs_page")
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
	t.Cleanup(func() { al.Close(context.Background()) })

	if err := al.RefreshMCPServer(context.Background(), "svc"); !errors.Is(err, clawmcp.ErrUnknownServer) {
		t.Fatalf("RefreshMCPServer without a manager = %v, want ErrUnknownServer", err)
	}
}
