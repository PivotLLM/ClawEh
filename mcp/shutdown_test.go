package mcp

import (
	"context"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/PivotLLM/ClawEh/config"
)

// After BeginShutdown a failed liveness probe must not reconnect: during a
// stop the signal kills stdio children, and respawning them only to kill them
// again delays the exit.
func TestProbeOnce_NoReconnectAfterBeginShutdown(t *testing.T) {
	good := newTestMCPServer(t)
	bad := httptest.NewServer(server.NewStreamableHTTPServer(server.NewMCPServer("bad", "0.0.1")))

	ctx := context.Background()
	mgr := NewManager()
	mgr.probeInterval = time.Hour
	defer func() {
		if err := mgr.Close(context.Background()); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()

	if err := mgr.ConnectServer(ctx, "svc", config.MCPServerConfig{
		Enabled: true, Type: "http", URL: bad.URL,
	}); err != nil {
		t.Fatalf("initial connect: %v", err)
	}
	conn, _ := mgr.GetServer("svc")
	conn.cfg.URL = good.URL
	bad.Close()

	mgr.BeginShutdown()
	if conn.probeStop != nil {
		t.Fatal("BeginShutdown must stop the liveness probe")
	}

	mgr.probeOnce("svc")

	conn2, ok := mgr.GetServer("svc")
	if !ok || conn2 != conn {
		t.Fatalf("probe must not reconnect after BeginShutdown: ok=%v same=%v", ok, conn2 == conn)
	}
	if got := mgr.RetryDisconnected(ctx); len(got) != 0 {
		t.Fatalf("RetryDisconnected connected %v after BeginShutdown", got)
	}
}

// Close must not wait past its context for a tool call that never returns: it
// names the busy server and closes the connections anyway.
func TestClose_BoundedByContext(t *testing.T) {
	release := make(chan struct{})
	srv := server.NewMCPServer("hang", "0.0.1")
	srv.AddTool(mcp.NewTool("hang", mcp.WithDescription("blocks")),
		func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			select {
			case <-release:
			case <-ctx.Done():
			}
			return &mcp.CallToolResult{}, nil
		})
	ts := httptest.NewServer(server.NewStreamableHTTPServer(srv))
	t.Cleanup(func() {
		close(release)
		ts.Close()
	})

	mgr := NewManager()
	if err := mgr.ConnectServer(context.Background(), "svc", config.MCPServerConfig{
		Enabled: true, Type: "http", URL: ts.URL,
	}); err != nil {
		t.Fatalf("connect: %v", err)
	}

	callErr := make(chan error, 1)
	go func() {
		_, err := mgr.CallTool(context.Background(), "svc", "hang", nil)
		callErr <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for !slices.Contains(mgr.busyServers(), "svc") {
		if time.Now().After(deadline) {
			t.Fatal("tool call never started")
		}
		time.Sleep(10 * time.Millisecond)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := mgr.Close(ctx); err != nil {
		t.Logf("Close: %v", err) // a hung session may fail to close cleanly; only the timing matters
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Close took %s; it must give up on a hung call when its context ends", elapsed)
	}
	if _, ok := mgr.GetServer("svc"); ok {
		t.Fatal("Close must drop the connection even when a call was still running")
	}
	select {
	case err := <-callErr:
		if err == nil {
			t.Error("the hung call succeeded; it should fail once its connection is closed")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the hung call did not end after its connection was closed")
	}
}
