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
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.Tools.ReadFile.Enabled = true
	cfg.Tools.WriteFile.Enabled = false
	cfg.Tools.Cron.Enabled = true
	cfg.Tools.FindSkills.Enabled = true
	cfg.Tools.Skills.Registry.Enabled = true
	cfg.Tools.Spawn.Enabled = true
	cfg.Tools.Subagent.Enabled = false
	cfg.Tools.MCP.Enabled = true
	cfg.Tools.MCP.Discovery.Enabled = true
	cfg.Tools.MCP.Discovery.UseRegex = true
	cfg.Tools.MCP.Discovery.UseBM25 = false
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
	if gotTools["read_file"].Status != "enabled" {
		t.Fatalf("read_file status = %q, want enabled", gotTools["read_file"].Status)
	}
	if gotTools["write_file"].Status != "disabled" {
		t.Fatalf("write_file status = %q, want disabled", gotTools["write_file"].Status)
	}
	if gotTools["cron"].Status != "enabled" {
		t.Fatalf("cron status = %q, want enabled", gotTools["cron"].Status)
	}
	if gotTools["spawn"].Status != "blocked" || gotTools["spawn"].ReasonCode != "requires_subagent" {
		t.Fatalf("spawn = %#v, want blocked/requires_subagent", gotTools["spawn"])
	}
	if gotTools["find_skills"].Status != "enabled" {
		t.Fatalf("find_skills status = %q, want enabled", gotTools["find_skills"].Status)
	}
	if gotTools["find_tools_regex"].Status != "enabled" {
		t.Fatalf("find_tools_regex status = %q, want enabled", gotTools["find_tools_regex"].Status)
	}
	if gotTools["find_tools_regex"].ConfigKey != "mcp.discovery.use_regex" {
		t.Fatalf(
			"find_tools_regex config_key = %q, want mcp.discovery.use_regex",
			gotTools["find_tools_regex"].ConfigKey,
		)
	}
	if gotTools["find_tools_bm25"].Status != "disabled" {
		t.Fatalf("find_tools_bm25 status = %q, want disabled", gotTools["find_tools_bm25"].Status)
	}
	if gotTools["find_tools_bm25"].ConfigKey != "mcp.discovery.use_bm25" {
		t.Fatalf(
			"find_tools_bm25 config_key = %q, want mcp.discovery.use_bm25",
			gotTools["find_tools_bm25"].ConfigKey,
		)
	}
	if gotTools["get_session_messages"].Status != "enabled" {
		t.Fatalf("get_session_messages status = %q, want enabled", gotTools["get_session_messages"].Status)
	}
	if gotTools["search_session_messages"].Status != "enabled" {
		t.Fatalf("search_session_messages status = %q, want enabled", gotTools["search_session_messages"].Status)
	}
	if runtime.GOOS == "linux" {
		if gotTools["i2c"].Status != "disabled" {
			t.Fatalf("i2c status = %q, want disabled on linux when config is off", gotTools["i2c"].Status)
		}
	} else {
		cfg.Tools.I2C.Enabled = true
		cfg.Tools.SPI.Enabled = true
		if err := config.SaveConfig(configPath, cfg); err != nil {
			t.Fatalf("SaveConfig() error = %v", err)
		}

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

		if gotTools["i2c"].Status != "blocked" || gotTools["i2c"].ReasonCode != "requires_linux" {
			t.Fatalf("i2c = %#v, want blocked/requires_linux", gotTools["i2c"])
		}
		if gotTools["spi"].Status != "blocked" || gotTools["spi"].ReasonCode != "requires_linux" {
			t.Fatalf("spi = %#v, want blocked/requires_linux", gotTools["spi"])
		}
	}
}

func TestHandleUpdateToolState(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.Tools.Spawn.Enabled = false
	cfg.Tools.Subagent.Enabled = false
	cfg.Tools.Cron.Enabled = false
	cfg.Tools.MCP.Enabled = false
	cfg.Tools.MCP.Discovery.Enabled = false
	cfg.Tools.MCP.Discovery.UseRegex = false
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
		"/api/tools/spawn/state",
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
		"/api/tools/find_tools_regex/state",
		bytes.NewBufferString(`{"enabled":true}`),
	)
	req2.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("regex status = %d, want %d, body=%s", rec2.Code, http.StatusOK, rec2.Body.String())
	}

	rec3 := httptest.NewRecorder()
	req3 := httptest.NewRequest(
		http.MethodPut,
		"/api/tools/cron/state",
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
	if !updated.Tools.Spawn.Enabled || !updated.Tools.Subagent.Enabled {
		t.Fatalf("spawn/subagent should both be enabled: %#v", updated.Tools)
	}
	if !updated.Tools.MCP.Enabled || !updated.Tools.MCP.Discovery.Enabled || !updated.Tools.MCP.Discovery.UseRegex {
		t.Fatalf("mcp regex discovery should be enabled: %#v", updated.Tools.MCP)
	}
	if !updated.Tools.Cron.Enabled {
		t.Fatalf("cron should be enabled: %#v", updated.Tools.Cron)
	}
}
