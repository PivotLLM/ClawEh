package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/PivotLLM/ClawEh/mcp"
)

type fakeMCPStatusLoop struct {
	servers    []mcp.ServerStatus
	refreshed  []string
	refreshErr error
}

func (f *fakeMCPStatusLoop) MCPStatus() []mcp.ServerStatus { return f.servers }

func (f *fakeMCPStatusLoop) RefreshMCPServer(_ context.Context, name string) error {
	f.refreshed = append(f.refreshed, name)
	return f.refreshErr
}

func TestMCPStatus_ReportsLiveServers(t *testing.T) {
	configPath := setupTestEnv(t)

	h := NewHandler(configPath)
	h.SetMCPStatusLoop(&fakeMCPStatusLoop{servers: []mcp.ServerStatus{
		{Name: "alice", State: mcp.StateConnected, Transport: "http", ToolCount: 3},
		{Name: "bob", State: mcp.StateCooldown},
	}})
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/mcp/status", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out mcpStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Servers) != 2 {
		t.Fatalf("expected 2 servers, got %d", len(out.Servers))
	}
	if out.Servers[0].Name != "alice" || out.Servers[0].State != mcp.StateConnected {
		t.Fatalf("unexpected first server: %+v", out.Servers[0])
	}
}

// TestMCPStatus_EmptyWhenLoopUnset returns an empty (non-null) list so the WebUI
// can treat every configured server as disconnected without special-casing null.
func TestMCPStatus_EmptyWhenLoopUnset(t *testing.T) {
	configPath := setupTestEnv(t)

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/mcp/status", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out mcpStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Servers == nil {
		t.Fatal("servers must serialize as [] not null")
	}
	if len(out.Servers) != 0 {
		t.Fatalf("expected empty servers, got %d", len(out.Servers))
	}
}

// TestMCPReconnect: the endpoint forwards the server name, maps an unknown
// server to 404 and any other failure to 502, and is 503 without a gateway.
func TestMCPReconnect(t *testing.T) {
	configPath := setupTestEnv(t)
	h := NewHandler(configPath)
	fake := &fakeMCPStatusLoop{}
	h.SetMCPStatusLoop(fake)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/mcp/servers/fusion/reconnect", nil))
	if rec.Code != http.StatusOK || len(fake.refreshed) != 1 || fake.refreshed[0] != "fusion" {
		t.Fatalf("status = %d, refreshed = %v, body=%s", rec.Code, fake.refreshed, rec.Body.String())
	}

	fake.refreshErr = mcp.ErrUnknownServer
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/mcp/servers/nope/reconnect", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown server: status = %d, want 404", rec.Code)
	}

	fake.refreshErr = errors.New("connect refused")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/mcp/servers/fusion/reconnect", nil))
	if rec.Code != http.StatusBadGateway {
		t.Errorf("connect failure: status = %d, want 502", rec.Code)
	}

	bare := NewHandler(configPath)
	mux = http.NewServeMux()
	bare.RegisterRoutes(mux)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/mcp/servers/fusion/reconnect", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no loop: status = %d, want 503", rec.Code)
	}
}
