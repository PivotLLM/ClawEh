package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/config"
)

func TestHandleGetConfig_ReturnsConfig(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var cfg config.Config
	if err := json.Unmarshal(rec.Body.Bytes(), &cfg); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
}

func TestHandleGetConfig_UnreadableConfigReturns500(t *testing.T) {
	// Use a directory as the config path — LoadConfig will fail to parse it
	dir := t.TempDir()
	h := NewHandler(dir) // dir is not a valid JSON file
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

func TestHandleUpdateConfig_InvalidJSONReturns400(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewBufferString(`not-json`))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHandleUpdateConfig_ValidationErrorReturns400(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// WebUI enabled without token should fail validation
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewBufferString(`{
		"agents": {"defaults": {"workspace": "~/.claw/workspace"}},
		"model_list": [{"model_name": "m", "model": "openai/gpt-4o", "api_key": "k"}],
		"channels": {"webui": {"enabled": true, "token": ""}}
	}`))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if body["status"] != "validation_error" {
		t.Fatalf("status = %q, want validation_error", body["status"])
	}
}

func TestHandlePatchConfig_PartialUpdate(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewBufferString(`{
		"gateway": {"port": 19000}
	}`))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if cfg.Gateway.Port != 19000 {
		t.Fatalf("gateway.port = %d, want 19000", cfg.Gateway.Port)
	}
}

func TestHandlePatchConfig_InvalidJSONReturns400(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewBufferString(`not-json`))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHandlePatchConfig_ValidationFailureReturns400(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// Patch in a gateway port that is out of range
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewBufferString(`{
		"gateway": {"port": 99999}
	}`))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestHandlePatchConfig_UnreadableConfigReturns500(t *testing.T) {
	// Use a directory as the config path — LoadConfig will fail to parse it
	dir := t.TempDir()
	h := NewHandler(dir) // dir is not a valid JSON file
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewBufferString(`{"gateway":{"port":0}}`))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

func TestValidateConfig_TelegramBotMissingToken(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ModelList = []config.ModelConfig{{
		ModelName: "m",
		Model:     "openai/gpt-4o",
		APIKey:    "k",
	}}
	cfg.Channels.Telegram = []config.TelegramBotConfig{{
		ID:      "bot1",
		Enabled: true,
		Token:   "",
	}}

	errs := validateConfig(cfg)
	if len(errs) == 0 {
		t.Fatal("expected validation errors for enabled telegram bot without token")
	}
	found := false
	for _, e := range errs {
		if containsStr(e, "bot1") {
			found = true
		}
	}
	if !found {
		t.Fatalf("errors = %v, expected error mentioning bot1", errs)
	}
}

func TestValidateConfig_DiscordMissingToken(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ModelList = []config.ModelConfig{{
		ModelName: "m",
		Model:     "openai/gpt-4o",
		APIKey:    "k",
	}}
	cfg.Channels.Discord.Enabled = true
	cfg.Channels.Discord.Token = ""

	errs := validateConfig(cfg)
	if len(errs) == 0 {
		t.Fatal("expected validation errors for enabled discord without token")
	}
}

func TestMergeMap_NullDeletesKey(t *testing.T) {
	dst := map[string]any{"a": "old", "b": "keep"}
	src := map[string]any{"a": nil}
	mergeMap(dst, src)

	if _, ok := dst["a"]; ok {
		t.Fatal("key 'a' should have been deleted by null patch")
	}
	if dst["b"] != "keep" {
		t.Fatalf("key 'b' = %v, want 'keep'", dst["b"])
	}
}

func TestMergeMap_RecursiveMerge(t *testing.T) {
	dst := map[string]any{
		"nested": map[string]any{"x": 1, "y": 2},
	}
	src := map[string]any{
		"nested": map[string]any{"x": 99},
	}
	mergeMap(dst, src)

	nested, ok := dst["nested"].(map[string]any)
	if !ok {
		t.Fatal("nested should remain a map")
	}
	if nested["x"] != 99 {
		t.Fatalf("nested.x = %v, want 99", nested["x"])
	}
	if nested["y"] != 2 {
		t.Fatalf("nested.y = %v, want 2 (should be preserved)", nested["y"])
	}
}

func TestMergeMap_OverwritesScalar(t *testing.T) {
	dst := map[string]any{"key": "old"}
	src := map[string]any{"key": "new"}
	mergeMap(dst, src)

	if dst["key"] != "new" {
		t.Fatalf("key = %v, want 'new'", dst["key"])
	}
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}
