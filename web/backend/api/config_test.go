package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
)

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

// TestHandleUpdateConfig_KeepsPreExistingDanglingModelReference: a reference
// to a missing model already in the file (left by an older release) does not
// block a PUT that keeps it and changes something else.
func TestHandleUpdateConfig_KeepsPreExistingDanglingModelReference(t *testing.T) {
	configPath := setupTestEnv(t)
	old, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	old.Agents.List[0].Models = []string{"DeepSeek 4 Pro", "custom-default"}
	if err = config.SaveConfig(configPath, old); err != nil {
		t.Fatal(err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewBufferString(`{
		"agents": {
			"defaults": {"models": ["custom-default"]},
			"list": [{"id": "main", "name": "Main", "default": true, "models": ["DeepSeek 4 Pro", "custom-default"]}]
		},
		"providers": [
			{"name": "openai", "protocol": "openai-chat", "base_url": "https://api.openai.com/v1", "api_key": "sk-default"}
		],
		"models": [
			{"model_name": "custom-default", "model": "gpt-4o", "provider": "openai", "enabled": true}
		],
		"logging": {"level": "warn"}
	}`))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Logging.Level != "warn" {
		t.Fatalf("logging.level = %q, want warn (the save was not applied)", cfg.Logging.Level)
	}
}

// A JSON Merge Patch null removes the key: the stored defaults temperature
// becomes nil and the file on disk no longer carries it.
func TestHandlePatchConfig_NullRemovesDefaultsTemperature(t *testing.T) {
	configPath := setupTestEnv(t)

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	temp := 0.7
	cfg.Agents.Defaults.Temperature = &temp
	if err = config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPatch, "/api/config",
		bytes.NewBufferString(`{"agents":{"defaults":{"temperature":null}}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	stored, err := h.currentConfig()
	if err != nil {
		t.Fatalf("currentConfig() error = %v", err)
	}
	if got := stored.Agents.Defaults.Temperature; got != nil {
		t.Fatalf("stored agents.defaults.temperature = %v, want nil", *got)
	}

	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var onDisk struct {
		Agents struct {
			Defaults map[string]json.RawMessage `json:"defaults"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatalf("Unmarshal(config file) error = %v", err)
	}
	if onDisk.Agents.Defaults == nil {
		t.Fatalf("config file has no agents.defaults: %s", raw)
	}
	if v, ok := onDisk.Agents.Defaults["temperature"]; ok {
		t.Fatalf("config file agents.defaults.temperature = %s, want no key", v)
	}
}

// The WebUI has no controls for security.permissions, so its saves must
// carry the block through unchanged: a PATCH of another field (the System
// page) and a PUT of what GET returned (the raw config page).
func TestConfigSavesKeepPermissions(t *testing.T) {
	configPath := setupTestEnv(t)
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Security.Permissions.Logs = config.AreaPermissions{DirMode: "0750", FileMode: "0640"}
	cfg.Security.Permissions.Agents = config.AreaPermissions{DirMode: "0770", FileMode: "0660"}
	if err = config.SaveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	want := cfg.Security.Permissions

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	serve := func(method, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "/api/config", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d, body=%s", method, rec.Code, rec.Body.String())
		}
		return rec
	}
	check := func(step string) {
		t.Helper()
		got, err := config.ReadPermissions(configPath)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("%s: security.permissions = %+v, want %+v", step, got, want)
		}
	}

	serve(http.MethodPatch, `{"logging":{"level":"warn"}}`)
	check("PATCH")
	got := serve(http.MethodGet, "")
	serve(http.MethodPut, got.Body.String())
	check("PUT of GET")
}

// PATCH /api/config refuses a body naming any key the config does not have,
// anywhere in the document, with 400 naming every such key, and applies
// nothing; free-form maps take any key, and a valid patch is unaffected.
func TestHandlePatchConfig_UnknownKeys(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr string // "" = accepted
	}{
		{"unknown top-level key", `{"logging":{"level":"warn"},"nonsense":1}`, "Unknown config key: nonsense"},
		{"unknown nested key", `{"logging":{"level":"warn","colour":true}}`, "Unknown config key: logging.colour"},
		{
			"removed permission keys", `{"security":{"permissions":{"dir_mode":"0750","file_mode":"0640"}}}`,
			"Unknown config keys: security.permissions.dir_mode, security.permissions.file_mode",
		},
		{
			"several at once", `{"zz":1,"gateway":{"prot":1},"logging":{"level":"warn"}}`,
			"Unknown config keys: gateway.prot, zz",
		},
		{
			"unknown key inside a list entry", `{"agents":{"list":[{"id":"alice","colour":"red"}]}}`,
			"Unknown config key: agents.list[0].colour",
		},
		{"valid patch", `{"logging":{"level":"warn"}}`, ""},
		{"null deletes a known key", `{"agents":{"defaults":{"temperature":null}}}`, ""},
		{"free-form maps take any key", `{"tools":{"mcp":{"servers":{"alice-files":{"enabled":false,"command":"true",` +
			`"env":{"ANY_NAME":"1"},"headers":{"X-Custom":"y"}}}}}}`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			configPath := setupTestEnv(t)
			before, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatal(err)
			}
			h := NewHandler(configPath)
			mux := http.NewServeMux()
			h.RegisterRoutes(mux)
			req := httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewBufferString(c.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if c.wantErr == "" {
				if rec.Code != http.StatusOK {
					t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
				}
				return
			}
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400, body=%s", rec.Code, rec.Body.String())
			}
			var resp struct {
				Errors []string `json:"errors"`
			}
			if err = json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatal(err)
			}
			if len(resp.Errors) != 1 || resp.Errors[0] != c.wantErr {
				t.Errorf("errors = %q, want [%q]", resp.Errors, c.wantErr)
			}
			after, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Error("a refused patch changed config.json")
			}
		})
	}
}
