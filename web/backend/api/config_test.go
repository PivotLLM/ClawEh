package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
)

func TestHandleUpdateConfig_AppliesExecAllowRemoteDefaultWhenOmitted(t *testing.T) {
	configPath := setupTestEnv(t)

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewBufferString(`{
		"agents": {
			"defaults": {
				"workspace": "~/.claw/workspace"
			},
			"list": [{"id": "main", "name": "Main", "default": true}]
		},
		"providers": [
			{
				"name": "openai",
				"protocol": "openai-chat",
				"base_url": "https://api.openai.com/v1",
				"api_key": "sk-default"
			}
		],
		"models": [
			{
				"model_name": "custom-default",
				"model": "gpt-4o",
				"provider": "openai",
				"enabled": true
			}
		]
	}`))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if cfg.Tools.Exec.AllowRemote {
		t.Fatal("tools.exec.allow_remote should take its default (false) when omitted from PUT /api/config")
	}
}

func TestHandleUpdateConfig_DoesNotInheritDefaultModelFields(t *testing.T) {
	configPath := setupTestEnv(t)

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewBufferString(`{
		"agents": {
			"defaults": {
				"workspace": "~/.claw/workspace"
			},
			"list": [{"id": "main", "name": "Main", "default": true}]
		},
		"providers": [
			{
				"name": "openai",
				"protocol": "openai-chat",
				"base_url": "https://api.openai.com/v1",
				"api_key": "sk-default"
			}
		],
		"models": [
			{
				"model_name": "custom-default",
				"model": "gpt-4o",
				"provider": "openai",
				"enabled": true
			}
		]
	}`))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if got := cfg.Models[0].ConnectMode; got != "" {
		t.Fatalf("models[0].connect_mode = %q, want empty string (not inherited from default template)", got)
	}
	if got := cfg.Models[0].Workspace; got != "" {
		t.Fatalf("models[0].workspace = %q, want empty string (not inherited from default template)", got)
	}
}

func TestHandleUpdateConfig_RejectsUnknownModelReference(t *testing.T) {
	configPath := setupTestEnv(t)

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewBufferString(`{
		"agents": {
			"defaults": {"models": ["custom-default"]},
			"list": [{"id": "alice", "name": "Alice", "default": true, "models": ["DeepSeek 4 Pro"]}]
		},
		"providers": [
			{
				"name": "openai",
				"protocol": "openai-chat",
				"base_url": "https://api.openai.com/v1",
				"api_key": "sk-default"
			}
		],
		"models": [
			{
				"model_name": "custom-default",
				"model": "gpt-4o",
				"provider": "openai",
				"enabled": true
			}
		]
	}`))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"agents.list[alice].models", "DeepSeek 4 Pro", "does not exist"} {
		if !strings.Contains(body, want) {
			t.Errorf("body %q does not contain %q", body, want)
		}
	}

	// The rejected config must not have been saved.
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if len(cfg.Agents.List) != 1 || cfg.Agents.List[0].ID != "main" {
		t.Fatalf("agents.list = %+v, want the original main agent", cfg.Agents.List)
	}
}
