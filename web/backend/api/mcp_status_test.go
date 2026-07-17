package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/mcp"
)

type fakeMCPStatusLoop struct {
	servers []mcp.ServerStatus
}

func (f *fakeMCPStatusLoop) MCPStatus() []mcp.ServerStatus { return f.servers }

func TestMCPStatus_ReportsLiveServers(t *testing.T) {
	configPath, cleanup := setupTestEnv(t)
	defer cleanup()

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
	configPath, cleanup := setupTestEnv(t)
	defer cleanup()

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
