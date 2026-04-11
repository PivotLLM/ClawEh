package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/config"
)

func TestHandleAddModel_Success(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	body := `{"model_name":"new-model","model":"openai/gpt-4o","api_key":"sk-new","enabled":true}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/models", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	found := false
	for _, m := range cfg.ModelList {
		if m.ModelName == "new-model" {
			found = true
		}
	}
	if !found {
		t.Fatal("new-model not found in model_list after add")
	}
}

func TestHandleAddModel_InvalidJSONReturns400(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/models", bytes.NewBufferString(`not-json`))
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHandleAddModel_ValidationErrorReturns400(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// Missing model_name should fail validation
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/models", bytes.NewBufferString(`{"model":"openai/gpt-4o"}`))
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestHandleUpdateModel_Success(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPut,
		"/api/models/0",
		bytes.NewBufferString(`{"model_name":"custom-default","model":"openai/gpt-4o","api_key":"","enabled":true}`),
	)
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	// Verify original key is preserved when empty key sent
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if cfg.ModelList[0].APIKey != "sk-default" {
		t.Fatalf("api_key = %q, want sk-default (preserved)", cfg.ModelList[0].APIKey)
	}
}

func TestHandleUpdateModel_InvalidIndexReturns404(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPut,
		"/api/models/999",
		bytes.NewBufferString(`{"model_name":"x","model":"openai/gpt-4o"}`),
	)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestHandleUpdateModel_InvalidIndexStringReturns400(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPut,
		"/api/models/notanumber",
		bytes.NewBufferString(`{}`),
	)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHandleUpdateModel_MaskedKeyPreservesOriginal(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// Send a masked key (contains "****") — original should be preserved
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPut,
		"/api/models/0",
		bytes.NewBufferString(`{"model_name":"custom-default","model":"openai/gpt-4o","api_key":"sk-****efgh","enabled":true}`),
	)
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if cfg.ModelList[0].APIKey != "sk-default" {
		t.Fatalf("api_key = %q, want sk-default (masked key should preserve original)", cfg.ModelList[0].APIKey)
	}
}

func TestHandleDeleteModel_Success(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/models/0", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if len(cfg.ModelList) != 0 {
		t.Fatalf("model_list len = %d, want 0 after delete", len(cfg.ModelList))
	}
}

func TestHandleDeleteModel_InvalidIndexReturns404(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/models/999", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestHandleDeleteModel_InvalidIndexStringReturns400(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/models/notanumber", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHandleDeleteModel_ClearsDefaultWhenDefaultDeleted(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	// Confirm the default model is set to custom-default
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if cfg.Agents.Defaults.DefaultModelName() != "custom-default" {
		t.Fatalf("default model = %q, want custom-default", cfg.Agents.Defaults.DefaultModelName())
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/models/0", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	cfg2, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if cfg2.Agents.Defaults.DefaultModelName() != "" {
		t.Fatalf("default model = %q, want empty after deleting default", cfg2.Agents.Defaults.DefaultModelName())
	}
}

func TestHandleSetDefaultModel_Success(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/models/default",
		bytes.NewBufferString(`{"model_name":"custom-default"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
}

func TestHandleSetDefaultModel_NotFoundReturns404(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/models/default",
		bytes.NewBufferString(`{"model_name":"does-not-exist"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestHandleSetDefaultModel_EmptyNameReturns400(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/models/default",
		bytes.NewBufferString(`{"model_name":""}`),
	)
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHandleSetDefaultModel_DisabledModelReturns400(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	// Add a disabled model
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.ModelList = append(cfg.ModelList, config.ModelConfig{
		ModelName: "disabled-model",
		Model:     "openai/gpt-4o",
		APIKey:    "sk-x",
		Enabled:   false,
	})
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/models/default",
		bytes.NewBufferString(`{"model_name":"disabled-model"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestMaskAPIKey(t *testing.T) {
	tests := []struct {
		key  string
		want string
	}{
		{"", ""},
		{"short", "****"},
		{"12345678", "****"},
		{"sk-abcdefghijklm", "sk-****jklm"},
	}
	for _, tc := range tests {
		t.Run(fmt.Sprintf("key=%q", tc.key), func(t *testing.T) {
			got := maskAPIKey(tc.key)
			if got != tc.want {
				t.Fatalf("maskAPIKey(%q) = %q, want %q", tc.key, got, tc.want)
			}
		})
	}
}

func TestHandleListModels_ReturnsModels(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	resetModelProbeHooks(t)

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/models", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Models       []modelResponse `json:"models"`
		Total        int             `json:"total"`
		DefaultModel string          `json:"default_model"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Total != 1 {
		t.Fatalf("total = %d, want 1", resp.Total)
	}
	if resp.DefaultModel != "custom-default" {
		t.Fatalf("default_model = %q, want custom-default", resp.DefaultModel)
	}
	// API key should be masked, not returned in plaintext
	if resp.Models[0].APIKey == "" {
		t.Fatal("api_key should be masked but got empty string")
	}
	if resp.Models[0].APIKey == "sk-default" {
		t.Fatal("api_key should be masked, not returned in plaintext")
	}
}
