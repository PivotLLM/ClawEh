package mcp

import (
	"context"
	"errors"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/mark3labs/mcp-go/server/servertest"

	"github.com/PivotLLM/ClawEh/config"
)

// noopHandler answers any tool call with an empty result.
func noopHandler(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return &mcp.CallToolResult{}, nil
}

// newTestMCPServerHandle is newTestMCPServer with the underlying mcp-go server
// exposed, so a test can change its tool list after a connection is up.
func newTestMCPServerHandle(t *testing.T) (*server.MCPServer, *httptest.Server) {
	t.Helper()
	srv := server.NewMCPServer("test", "0.0.1")
	srv.AddTool(mcp.NewTool("ping", mcp.WithDescription("no-op")), noopHandler)
	ts := httptest.NewServer(server.NewStreamableHTTPServer(srv))
	t.Cleanup(ts.Close)
	return srv, ts
}

// newChangeCapturingManager returns a manager whose tools-changed handler
// delivers the server name on the returned channel.
func newChangeCapturingManager(t *testing.T) (*Manager, chan string) {
	t.Helper()
	mgr := NewManager()
	changed := make(chan string, 8)
	mgr.SetToolsChangedHandler(func(server string) { changed <- server })
	t.Cleanup(func() {
		if err := mgr.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return mgr, changed
}

// waitChanged fails the test unless the handler fires for server within the
// timeout.
func waitChanged(t *testing.T, changed <-chan string, server string, timeout time.Duration) {
	t.Helper()
	select {
	case got := <-changed:
		if got != server {
			t.Fatalf("handler fired for %q, want %q", got, server)
		}
	case <-time.After(timeout):
		t.Fatalf("tools-changed handler did not fire for %q within %s", server, timeout)
	}
}

// storedToolNames returns the sorted names of the tools stored for server.
func storedToolNames(t *testing.T, mgr *Manager, server string) []string {
	t.Helper()
	conn, ok := mgr.GetServer(server)
	if !ok {
		t.Fatalf("%s should be connected", server)
	}
	names := make([]string, 0, len(conn.Tools))
	for _, tool := range conn.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	return names
}

func TestToolsEqual(t *testing.T) {
	ping := mcp.NewTool("ping", mcp.WithDescription("no-op"))
	pong := mcp.NewTool("pong", mcp.WithDescription("no-op"))
	pingRenamedDesc := mcp.NewTool("ping", mcp.WithDescription("changed"))
	pingNewSchema := mcp.NewTool("ping", mcp.WithDescription("no-op"), mcp.WithString("q"))

	cases := []struct {
		name string
		a, b []mcp.Tool
		want bool
	}{
		{"both empty", nil, nil, true},
		{"same order", []mcp.Tool{ping, pong}, []mcp.Tool{ping, pong}, true},
		{"different order", []mcp.Tool{ping, pong}, []mcp.Tool{pong, ping}, true},
		{"tool added", []mcp.Tool{ping}, []mcp.Tool{ping, pong}, false},
		{"tool removed", []mcp.Tool{ping, pong}, []mcp.Tool{ping}, false},
		{"description changed", []mcp.Tool{ping}, []mcp.Tool{pingRenamedDesc}, false},
		{"schema changed", []mcp.Tool{ping}, []mcp.Tool{pingNewSchema}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := toolsEqual(tc.a, tc.b); got != tc.want {
				t.Fatalf("toolsEqual = %v, want %v", got, tc.want)
			}
		})
	}
}

// A probe whose tools/list answer differs from the stored list installs the new
// list and fires the handler.
func TestProbeOnce_RefreshesChangedToolList(t *testing.T) {
	srv, ts := newTestMCPServerHandle(t)
	mgr, changed := newChangeCapturingManager(t)

	if err := mgr.ConnectServer(context.Background(), "svc", config.MCPServerConfig{
		Enabled: true, Type: "http", URL: ts.URL,
	}); err != nil {
		t.Fatalf("connect: %v", err)
	}

	srv.AddTool(mcp.NewTool("pong", mcp.WithDescription("no-op")), noopHandler)
	mgr.probeOnce("svc")

	waitChanged(t, changed, "svc", 5*time.Second)
	if got, want := storedToolNames(t, mgr, "svc"), []string{"ping", "pong"}; !slices.Equal(got, want) {
		t.Fatalf("stored tools after probe = %v, want %v", got, want)
	}
}

// A tools/list_changed notification from the server re-lists and fires the
// handler. The SSE transport carries the notification on the connection's own
// stream (mcp-go's server broadcasts it to registered sessions on AddTool).
func TestToolsListChangedNotification_RefreshesToolList(t *testing.T) {
	srv := server.NewMCPServer("test", "0.0.1")
	srv.AddTool(mcp.NewTool("ping", mcp.WithDescription("no-op")), noopHandler)
	ts := servertest.NewTestServer(srv)
	t.Cleanup(ts.Close)
	mgr, changed := newChangeCapturingManager(t)

	if err := mgr.ConnectServer(context.Background(), "svc", config.MCPServerConfig{
		Enabled: true, Type: "sse", URL: ts.URL + "/sse",
	}); err != nil {
		t.Fatalf("connect: %v", err)
	}

	srv.AddTool(mcp.NewTool("pong", mcp.WithDescription("no-op")), noopHandler)

	waitChanged(t, changed, "svc", 10*time.Second)
	if got, want := storedToolNames(t, mgr, "svc"), []string{"ping", "pong"}; !slices.Equal(got, want) {
		t.Fatalf("stored tools after notification = %v, want %v", got, want)
	}
}

func TestReconnect_UnknownServer(t *testing.T) {
	mgr, _ := newChangeCapturingManager(t)
	if err := mgr.Reconnect(context.Background(), "nope"); !errors.Is(err, ErrUnknownServer) {
		t.Fatalf("Reconnect(unknown) = %v, want ErrUnknownServer", err)
	}
}

// Reconnect on a configured server reconnects it, clears any cooldown on the
// way, and fires the handler when the list came back different.
func TestReconnect_KnownServerFiresHandlerWhenChanged(t *testing.T) {
	srv, ts := newTestMCPServerHandle(t)
	mgr, changed := newChangeCapturingManager(t)

	ctx := context.Background()
	if err := mgr.LoadFromMCPConfig(ctx, config.MCPConfig{
		Servers: map[string]config.MCPServerConfig{
			"svc": {Enabled: true, Type: "http", URL: ts.URL},
		},
	}, ""); err != nil {
		t.Fatalf("load: %v", err)
	}
	old, _ := mgr.GetServer("svc")

	srv.DeleteTools("ping")
	srv.AddTool(mcp.NewTool("pong", mcp.WithDescription("no-op")), noopHandler)

	// An explicit reconnect is forced through a post-failure cooldown.
	mgr.markReconnectFailed("svc")
	if err := mgr.Reconnect(ctx, "svc"); err != nil {
		t.Fatalf("Reconnect: %v", err)
	}
	if _, cooling := mgr.reconnectCooldownUntil("svc"); cooling {
		t.Fatal("a successful forced reconnect must leave no cooldown behind")
	}
	if fresh, _ := mgr.GetServer("svc"); fresh == old {
		t.Fatal("Reconnect must replace the connection")
	}

	waitChanged(t, changed, "svc", 5*time.Second)
	if got, want := storedToolNames(t, mgr, "svc"), []string{"pong"}; !slices.Equal(got, want) {
		t.Fatalf("stored tools after reconnect = %v, want %v", got, want)
	}
}

// Neither a probe nor a reconnect fires the handler when the list is identical.
func TestToolsChangedHandler_NotFiredWhenIdentical(t *testing.T) {
	_, ts := newTestMCPServerHandle(t)
	mgr, changed := newChangeCapturingManager(t)

	ctx := context.Background()
	if err := mgr.LoadFromMCPConfig(ctx, config.MCPConfig{
		Servers: map[string]config.MCPServerConfig{
			"svc": {Enabled: true, Type: "http", URL: ts.URL},
		},
	}, ""); err != nil {
		t.Fatalf("load: %v", err)
	}

	mgr.probeOnce("svc")
	if err := mgr.Reconnect(ctx, "svc"); err != nil {
		t.Fatalf("Reconnect: %v", err)
	}

	select {
	case got := <-changed:
		t.Fatalf("handler fired for %q although the tool list is unchanged", got)
	case <-time.After(300 * time.Millisecond):
	}
	if got, want := storedToolNames(t, mgr, "svc"), []string{"ping"}; !slices.Equal(got, want) {
		t.Fatalf("stored tools = %v, want %v", got, want)
	}
}
