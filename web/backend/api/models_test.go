package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/auth"
	"github.com/PivotLLM/ClawEh/pkg/config"
)

func resetModelProbeHooks(t *testing.T) {
	t.Helper()

	origTCPProbe := probeTCPServiceFunc
	origOllamaProbe := probeOllamaModelFunc
	origOpenAIProbe := probeOpenAICompatibleModelFunc
	t.Cleanup(func() {
		probeTCPServiceFunc = origTCPProbe
		probeOllamaModelFunc = origOllamaProbe
		probeOpenAICompatibleModelFunc = origOpenAIProbe
	})
}

func TestHandleListModels_ConfiguredStatusUsesRuntimeProbesForLocalModels(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	resetOAuthHooks(t)
	resetModelProbeHooks(t)

	// Only providers with a local base_url get runtime-probed; that probe always
	// goes through the openai-compatible /models endpoint. Remote api-key
	// providers are considered configured without any network probe, and an
	// oauth/token anthropic provider is unconfigured until a credential exists.
	var mu sync.Mutex
	var openAIProbes []string

	probeOpenAICompatibleModelFunc = func(apiBase, modelID string) bool {
		mu.Lock()
		openAIProbes = append(openAIProbes, apiBase+"|"+modelID)
		mu.Unlock()
		return apiBase == "http://127.0.0.1:8000/v1" && modelID == "custom-model"
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.Providers = []config.Provider{
		{Name: "anthropic", Protocol: "anthropic", BaseURL: "https://api.anthropic.com/v1", AuthMethod: "token"},
		{Name: "vllm-local", Protocol: "openai-chat", BaseURL: "http://127.0.0.1:8000/v1"},
		{Name: "vllm-remote", Protocol: "openai-chat", BaseURL: "https://models.example.com/v1", APIKey: "remote-key"},
	}
	cfg.Models = []config.ModelConfig{
		{ModelName: "anthropic-token", Model: "claude-sonnet-4.6", Provider: "anthropic", Enabled: true},
		{ModelName: "vllm-local", Model: "custom-model", Provider: "vllm-local", Enabled: true},
		{ModelName: "vllm-remote", Model: "custom-model", Provider: "vllm-remote", Enabled: true},
	}
	cfg.Agents.Defaults.SetDefaultModel("anthropic-token")
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

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
		Models []modelResponse `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	got := make(map[string]bool, len(resp.Models))
	for _, model := range resp.Models {
		got[model.ModelName] = model.Configured
	}

	if got["anthropic-token"] {
		t.Fatalf("anthropic token model configured = true, want false without stored credential")
	}
	if !got["vllm-local"] {
		t.Fatalf("vllm local model configured = false, want true when local probe succeeds")
	}
	if !got["vllm-remote"] {
		t.Fatalf("remote vllm model configured = false, want true with api_key")
	}
	if len(openAIProbes) != 1 || openAIProbes[0] != "http://127.0.0.1:8000/v1|custom-model" {
		t.Fatalf("openAI probes = %#v, want only local vllm probe", openAIProbes)
	}
}

func TestHandleListModels_ConfiguredStatusForOAuthModelWithCredential(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	resetOAuthHooks(t)
	resetModelProbeHooks(t)

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.Providers = []config.Provider{{
		Name:       "anthropic",
		Protocol:   "anthropic",
		BaseURL:    "https://api.anthropic.com/v1",
		AuthMethod: "oauth",
	}}
	cfg.Models = []config.ModelConfig{{
		ModelName: "claude-oauth",
		Model:     "claude-sonnet-4.6",
		Provider:  "anthropic",
		Enabled:   true,
	}}
	cfg.Agents.Defaults.SetDefaultModel("claude-oauth")
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	if err := auth.SetCredential(oauthProviderAnthropic, &auth.AuthCredential{
		AccessToken: "anthropic-token",
		Provider:    oauthProviderAnthropic,
		AuthMethod:  "oauth",
	}); err != nil {
		t.Fatalf("SetCredential() error = %v", err)
	}

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
		Models []modelResponse `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(resp.Models) != 1 {
		t.Fatalf("len(models) = %d, want 1", len(resp.Models))
	}
	if !resp.Models[0].Configured {
		t.Fatalf("oauth model configured = false, want true with stored credential")
	}
}

func TestHandleListModels_ProbesLocalModelsConcurrently(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	resetOAuthHooks(t)
	resetModelProbeHooks(t)

	started := make(chan string, 2)
	release := make(chan struct{})

	probeOpenAICompatibleModelFunc = func(apiBase, modelID string) bool {
		started <- apiBase + "|" + modelID
		<-release
		return true
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.Providers = []config.Provider{
		{Name: "vllm-a", Protocol: "openai-chat", BaseURL: "http://127.0.0.1:8000/v1"},
		{Name: "vllm-b", Protocol: "openai-chat", BaseURL: "http://127.0.0.1:8001/v1"},
	}
	cfg.Models = []config.ModelConfig{
		{ModelName: "local-vllm-a", Model: "custom-a", Provider: "vllm-a", Enabled: true},
		{ModelName: "local-vllm-b", Model: "custom-b", Provider: "vllm-b", Enabled: true},
	}
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	recCh := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/models", nil)
		mux.ServeHTTP(rec, req)
		recCh <- rec
	}()

	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(200 * time.Millisecond):
			t.Fatal("expected both local probes to start before the first one completed")
		}
	}
	close(release)

	rec := <-recCh
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
}

func TestHandleListModels_NormalizesWildcardLocalAPIBaseForProbe(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	resetOAuthHooks(t)
	resetModelProbeHooks(t)

	var gotProbe string
	probeOpenAICompatibleModelFunc = func(apiBase, modelID string) bool {
		gotProbe = apiBase + "|" + modelID
		return apiBase == "http://127.0.0.1:8000/v1" && modelID == "custom-model"
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.Providers = []config.Provider{{
		Name:     "vllm-local",
		Protocol: "openai-chat",
		BaseURL:  "http://0.0.0.0:8000/v1",
	}}
	cfg.Models = []config.ModelConfig{{
		ModelName: "vllm-local",
		Model:     "custom-model",
		Provider:  "vllm-local",
		Enabled:   true,
	}}
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

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
		Models []modelResponse `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(resp.Models) != 1 {
		t.Fatalf("len(models) = %d, want 1", len(resp.Models))
	}
	if !resp.Models[0].Configured {
		t.Fatal("wildcard-bound local model configured = false, want true after probe host normalization")
	}
	if gotProbe != "http://127.0.0.1:8000/v1|custom-model" {
		t.Fatalf("probe api base = %q, want %q", gotProbe, "http://127.0.0.1:8000/v1|custom-model")
	}
}
