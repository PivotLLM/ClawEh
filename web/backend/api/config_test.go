package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/config"
)

func TestHandleUpdateConfig_AppliesExecAllowRemoteDefaultWhenOmitted(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

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
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

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
