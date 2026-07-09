package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/config"
)

func TestHandleListTools(t *testing.T) {
	configPath, cleanup := setupTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	// Per-tool enable state lives in the generic override map now; file_read_lines is
	// default-allow so it stays enabled, file_write is overridden off, and
	// skill_find (default-deny) is overridden on.
	cfg.Tools.Overrides = map[string]bool{"file_write": false, "skill_find": true}
	cfg.Tools.Cron.Enabled = true
	cfg.Tools.Skills.Registry.Enabled = true
	cfg.Tools.Subagent.Enabled = false
	cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{
		"s": {Enabled: true, Command: "echo"},
	}
	err = config.SaveConfig(configPath, cfg)
	if err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/tools", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp toolSupportResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	gotTools := make(map[string]toolSupportItem, len(resp.Tools))
	for _, tool := range resp.Tools {
		gotTools[tool.Name] = tool
	}
	if gotTools["file_read_lines"].Status != "enabled" {
		t.Fatalf("file_read_lines status = %q, want enabled", gotTools["file_read_lines"].Status)
	}
	// Byte-addressed file tools are default-off now (edge-case).
	if gotTools["file_read_bytes"].Status != "disabled" {
		t.Fatalf("file_read_bytes status = %q, want disabled (default off)", gotTools["file_read_bytes"].Status)
	}
	if gotTools["file_write"].Status != "disabled" {
		t.Fatalf("file_write status = %q, want disabled", gotTools["file_write"].Status)
	}
	if gotTools["cron_schedule"].Status != "enabled" {
		t.Fatalf("cron status = %q, want enabled", gotTools["cron_schedule"].Status)
	}
	if gotTools["agent_spawn"].Status != "blocked" || gotTools["agent_spawn"].ReasonCode != "requires_subagent" {
		t.Fatalf("agent_spawn = %#v, want blocked/requires_subagent", gotTools["agent_spawn"])
	}
	if gotTools["skill_find"].Status != "enabled" {
		t.Fatalf("skill_find status = %q, want enabled", gotTools["skill_find"].Status)
	}
	if gotTools["session_messages"].Status != "enabled" {
		t.Fatalf("session_messages status = %q, want enabled", gotTools["session_messages"].Status)
	}
	if gotTools["session_search"].Status != "enabled" {
		t.Fatalf("session_search status = %q, want enabled", gotTools["session_search"].Status)
	}
	if runtime.GOOS == "linux" {
		if gotTools["hw_i2c"].Status != "disabled" {
			t.Fatalf("hw_i2c status = %q, want disabled on linux when config is off", gotTools["hw_i2c"].Status)
		}
	} else {
		rec = httptest.NewRecorder()
		req = httptest.NewRequest(http.MethodGet, "/api/tools", nil)
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
		}

		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("Unmarshal() error = %v", err)
		}
		gotTools = make(map[string]toolSupportItem, len(resp.Tools))
		for _, tool := range resp.Tools {
			gotTools[tool.Name] = tool
		}

		if gotTools["hw_i2c"].Status != "blocked" || gotTools["hw_i2c"].ReasonCode != "requires_linux" {
			t.Fatalf("hw_i2c = %#v, want blocked/requires_linux", gotTools["hw_i2c"])
		}
		if gotTools["hw_spi"].Status != "blocked" || gotTools["hw_spi"].ReasonCode != "requires_linux" {
			t.Fatalf("hw_spi = %#v, want blocked/requires_linux", gotTools["hw_spi"])
		}
	}
}

func TestHandleUpdateToolState(t *testing.T) {
	configPath, cleanup := setupTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.Tools.Subagent.Enabled = false
	cfg.Tools.Cron.Enabled = false
	err = config.SaveConfig(configPath, cfg)
	if err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPut,
		"/api/tools/agent_spawn/state",
		bytes.NewBufferString(`{"enabled":true}`),
	)
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("spawn status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(
		http.MethodPut,
		"/api/tools/file_read_bytes/state",
		bytes.NewBufferString(`{"enabled":true}`),
	)
	req2.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("file_read_bytes status = %d, want %d, body=%s", rec2.Code, http.StatusOK, rec2.Body.String())
	}

	rec3 := httptest.NewRecorder()
	req3 := httptest.NewRequest(
		http.MethodPut,
		"/api/tools/cron_schedule/state",
		bytes.NewBufferString(`{"enabled":true}`),
	)
	req3.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Fatalf("cron status = %d, want %d, body=%s", rec3.Code, http.StatusOK, rec3.Body.String())
	}

	updated, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig(updated) error = %v", err)
	}
	// Namespaced tools (agent_spawn, cron_schedule) toggle via the generic
	// override map now, not the legacy typed fields.
	if !updated.Tools.Overrides["agent_spawn"] {
		t.Fatalf("agent_spawn override should be true: %#v", updated.Tools.Overrides)
	}
	if !updated.Tools.Overrides["file_read_bytes"] {
		t.Fatalf("file_read_bytes override should be true: %#v", updated.Tools.Overrides)
	}
	if !updated.Tools.Overrides["cron_schedule"] {
		t.Fatalf("cron_schedule override should be true: %#v", updated.Tools.Overrides)
	}
}
