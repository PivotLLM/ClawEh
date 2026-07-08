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
	configPath, cleanup := setupTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	body := `{"model_name":"new-model","model":"gpt-4o","provider":"openai","enabled":true}`
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
	for _, m := range cfg.Models {
		if m.ModelName == "new-model" {
			found = true
		}
	}
	if !found {
		t.Fatal("new-model not found in models after add")
	}
}

// TestHandleAddModel_ContextWindowRoundTrips verifies a per-model context_window
// is persisted on add and surfaced in the list response (tokens).
func TestHandleAddModel_ContextWindowRoundTrips(t *testing.T) {
	configPath, cleanup := setupTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	body := `{"model_name":"cw-model","model":"gpt-4o","provider":"openai","context_window":200000,"enabled":true}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/models", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("add status = %d, body=%s", rec.Code, rec.Body.String())
	}

	// Persisted to config.
	cfg, _ := config.LoadConfig(configPath)
	var cw int
	for _, m := range cfg.Models {
		if m.ModelName == "cw-model" {
			cw = m.ContextWindow
		}
	}
	if cw != 200000 {
		t.Fatalf("persisted context_window = %d, want 200000", cw)
	}

	// Surfaced in the list response.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/models", nil))
	var resp struct {
		Models []modelResponse `json:"models"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	found := false
	for _, m := range resp.Models {
		if m.ModelName == "cw-model" {
			found = true
			if m.ContextWindow != 200000 {
				t.Fatalf("list context_window = %d, want 200000", m.ContextWindow)
			}
		}
	}
	if !found {
		t.Fatal("cw-model not in list response")
	}
}

func TestHandleAddModel_InvalidJSONReturns400(t *testing.T) {
	configPath, cleanup := setupTestEnv(t)
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
	configPath, cleanup := setupTestEnv(t)
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
	configPath, cleanup := setupTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPut,
		"/api/models/0",
		bytes.NewBufferString(`{"model_name":"custom-default","model":"gpt-4o","provider":"openai","enabled":true}`),
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
	if cfg.Models[0].Model != "gpt-4o" || cfg.Models[0].Provider != "openai" {
		t.Fatalf("models[0] = %+v, want model=gpt-4o provider=openai", cfg.Models[0])
	}
}

// TestHandleUpdateModel_DropParamsRoundTrip verifies the WebUI wiring for the
// per-model drop_params filter: PUT persists the list, GET exposes it, and a
// subsequent empty-array PUT clears it (omitempty drops it on save).
func TestHandleUpdateModel_DropParamsRoundTrip(t *testing.T) {
	configPath, cleanup := setupTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// PUT with drop_params set.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPut,
		"/api/models/0",
		bytes.NewBufferString(`{"model_name":"custom-default","model":"gpt-4o","provider":"openai","drop_params":["temperature","top_p"]}`),
	)
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if got := cfg.Models[0].DropParams; len(got) != 2 || got[0] != "temperature" || got[1] != "top_p" {
		t.Fatalf("DropParams = %v, want [temperature top_p]", got)
	}

	// GET must expose it.
	recGet := httptest.NewRecorder()
	mux.ServeHTTP(recGet, httptest.NewRequest(http.MethodGet, "/api/models", nil))
	var listResp struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(recGet.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("Unmarshal list: %v", err)
	}
	dp, ok := listResp.Models[0]["drop_params"].([]any)
	if !ok || len(dp) != 2 {
		t.Fatalf("GET drop_params = %v, want 2 entries", listResp.Models[0]["drop_params"])
	}

	// PUT with [] clears it.
	recClear := httptest.NewRecorder()
	reqClear := httptest.NewRequest(
		http.MethodPut,
		"/api/models/0",
		bytes.NewBufferString(`{"model_name":"custom-default","model":"gpt-4o","provider":"openai","drop_params":[]}`),
	)
	reqClear.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(recClear, reqClear)
	if recClear.Code != http.StatusOK {
		t.Fatalf("clear PUT status = %d, want %d, body=%s", recClear.Code, http.StatusOK, recClear.Body.String())
	}
	cfg, err = config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() after clear error = %v", err)
	}
	if len(cfg.Models[0].DropParams) != 0 {
		t.Fatalf("DropParams after clear = %v, want empty", cfg.Models[0].DropParams)
	}
}

func TestHandleUpdateModel_InvalidIndexReturns404(t *testing.T) {
	configPath, cleanup := setupTestEnv(t)
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
	configPath, cleanup := setupTestEnv(t)
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

func TestHandleDeleteModel_Success(t *testing.T) {
	configPath, cleanup := setupTestEnv(t)
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
	if len(cfg.Models) != 0 {
		t.Fatalf("models len = %d, want 0 after delete", len(cfg.Models))
	}
}

func TestHandleDeleteModel_InvalidIndexReturns404(t *testing.T) {
	configPath, cleanup := setupTestEnv(t)
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
	configPath, cleanup := setupTestEnv(t)
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
	configPath, cleanup := setupTestEnv(t)
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
	configPath, cleanup := setupTestEnv(t)
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
	configPath, cleanup := setupTestEnv(t)
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
	configPath, cleanup := setupTestEnv(t)
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
	configPath, cleanup := setupTestEnv(t)
	defer cleanup()

	// Add a disabled model
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.Models = append(cfg.Models, config.ModelConfig{
		ModelName: "disabled-model",
		Model:     "gpt-4o",
		Provider:  "openai",
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
	configPath, cleanup := setupTestEnv(t)
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
	if resp.Models[0].Provider != "openai" {
		t.Fatalf("provider = %q, want openai", resp.Models[0].Provider)
	}
}

// TestHandleUpdateModel_VisionRoundTrip verifies the per-model vision enum
// wiring: PUT persists it, GET exposes it, and a later PUT that omits vision
// preserves the stored value. That preservation is the property the WebUI edit
// sheet relies on — handleUpdateModel merge-unmarshals onto the existing entry,
// so a body without "vision" must not silently clear a configured value.
func TestHandleUpdateModel_VisionRoundTrip(t *testing.T) {
	configPath, cleanup := setupTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	put := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/api/models/0", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		mux.ServeHTTP(rec, req)
		return rec
	}

	// PUT with vision set.
	if rec := put(`{"model_name":"custom-default","model":"gpt-4o","provider":"openai","vision":"user_message"}`); rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body=%s", rec.Code, rec.Body.String())
	}
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if cfg.Models[0].Vision != config.VisionUserMessage {
		t.Fatalf("Vision = %q, want %q", cfg.Models[0].Vision, config.VisionUserMessage)
	}

	// GET must expose it.
	recGet := httptest.NewRecorder()
	mux.ServeHTTP(recGet, httptest.NewRequest(http.MethodGet, "/api/models", nil))
	var listResp struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(recGet.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("Unmarshal list: %v", err)
	}
	if listResp.Models[0]["vision"] != "user_message" {
		t.Fatalf("GET vision = %v, want user_message", listResp.Models[0]["vision"])
	}

	// A later PUT that omits "vision" must preserve the stored value.
	if rec := put(`{"model_name":"custom-default","model":"gpt-4o","provider":"openai"}`); rec.Code != http.StatusOK {
		t.Fatalf("PUT(no vision) status = %d, body=%s", rec.Code, rec.Body.String())
	}
	cfg, err = config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if cfg.Models[0].Vision != config.VisionUserMessage {
		t.Fatalf("vision not preserved on omit: got %q, want %q", cfg.Models[0].Vision, config.VisionUserMessage)
	}
}
