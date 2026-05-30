// PicoClaw - Ultra-lightweight personal AI agent
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package providers

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/config"
)

// Factory should reject a protocol that isn't hardcoded AND isn't tagged via
// MarkOpenAICompatExtra — protects against operators forgetting to register
// the protocol in openai_compat_protocols.
func TestCreateProviderFromConfig_OpenAICompatExtra_UntaggedRejected(t *testing.T) {
	cfg := &config.ModelConfig{
		ModelName: "mv",
		Model:     "myvendor/foo",
		APIKey:    "k",
		APIBase:   "https://example.com/v1",
	}
	if _, _, err := CreateProviderFromConfig(cfg); err == nil {
		t.Fatal("expected unknown-protocol error when ModelConfig is not marked as openai-compat extra")
	}
}

// A registered openai-compat protocol with a per-model api_base override
// must build an *HTTPProvider and route requests at the override URL.
func TestCreateProviderFromConfig_OpenAICompatExtra_RoutesToOverride(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer server.Close()

	cfg := &config.ModelConfig{
		ModelName: "mv",
		Model:     "myvendor/foo",
		APIKey:    "k",
		APIBase:   server.URL,
	}
	// Default api_base registered as something OBVIOUSLY wrong — the test
	// proves the per-model override wins. A mutation that swaps the order
	// (i.e. registered default beats APIBase) would route requests to
	// http://wrong.invalid and the assertion below would catch it.
	cfg.MarkOpenAICompatExtra("http://wrong.invalid/should-not-be-used")

	provider, modelID, err := CreateProviderFromConfig(cfg)
	if err != nil {
		t.Fatalf("CreateProviderFromConfig: %v", err)
	}
	if _, ok := provider.(*HTTPProvider); !ok {
		t.Fatalf("expected *HTTPProvider, got %T", provider)
	}
	if modelID != "foo" {
		t.Errorf("modelID = %q, want foo", modelID)
	}

	if _, err := provider.Chat(
		t.Context(),
		[]Message{{Role: "user", Content: "hi"}},
		nil,
		modelID,
		nil,
	); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if hits.Load() == 0 {
		t.Fatal("server received no requests — per-model api_base override was not honored")
	}
}

// A registered openai-compat protocol with NO per-model api_base falls back
// to the registered default. Mutation guard: if the factory stops consulting
// OpenAICompatBase, no request hits the server and the assertion fails.
func TestCreateProviderFromConfig_OpenAICompatExtra_RoutesToRegisteredDefault(t *testing.T) {
	var hits atomic.Int32
	var lastPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		lastPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	cfg := &config.ModelConfig{
		ModelName: "mv",
		Model:     "myvendor/foo",
		APIKey:    "k",
	}
	cfg.MarkOpenAICompatExtra(server.URL)

	provider, modelID, err := CreateProviderFromConfig(cfg)
	if err != nil {
		t.Fatalf("CreateProviderFromConfig: %v", err)
	}
	if _, ok := provider.(*HTTPProvider); !ok {
		t.Fatalf("expected *HTTPProvider, got %T", provider)
	}
	if _, err := provider.Chat(
		t.Context(),
		[]Message{{Role: "user", Content: "hi"}},
		nil,
		modelID,
		nil,
	); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if hits.Load() == 0 {
		t.Fatal("server received no requests — registered default api_base was not honored")
	}
	if !strings.HasSuffix(lastPath, "/chat/completions") {
		t.Errorf("request path = %q, want suffix /chat/completions", lastPath)
	}
}

// Tagged extra protocol with no api_base anywhere → error.
func TestCreateProviderFromConfig_OpenAICompatExtra_NoBaseAnywhere(t *testing.T) {
	cfg := &config.ModelConfig{
		ModelName: "mv",
		Model:     "myvendor/foo",
		APIKey:    "k",
	}
	cfg.MarkOpenAICompatExtra("") // registered with empty default

	_, _, err := CreateProviderFromConfig(cfg)
	if err == nil {
		t.Fatal("expected error when no api_base is available (neither per-model nor registered default)")
	}
	if !strings.Contains(err.Error(), "myvendor") {
		t.Errorf("error should name the protocol: %v", err)
	}
}

// End-to-end: a model loaded from a JSON config that uses
// openai_compat_protocols should resolve to an HTTPProvider that targets the
// registered default URL.
func TestOpenAICompatProtocols_EndToEnd_RoutesViaLoadConfig(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	body := `{
		"agents": {"defaults": {"model": "mv"}},
		"openai_compat_protocols": {"myvendor": "` + server.URL + `"},
		"model_list": [
			{"model_name": "mv", "model": "myvendor/foo", "api_key": "k", "enabled": true}
		]
	}`
	dir := t.TempDir()
	path := dir + "/config.json"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	mc, err := cfg.GetModelConfig("mv")
	if err != nil {
		t.Fatalf("GetModelConfig: %v", err)
	}
	if !mc.IsOpenAICompatExtra() {
		t.Fatal("expected ModelConfig to be tagged as openai-compat extra after LoadConfig")
	}
	provider, modelID, err := CreateProviderFromConfig(mc)
	if err != nil {
		t.Fatalf("CreateProviderFromConfig: %v", err)
	}
	if _, err := provider.Chat(
		t.Context(),
		[]Message{{Role: "user", Content: "hi"}},
		nil,
		modelID,
		nil,
	); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if hits.Load() == 0 {
		t.Fatal("server received no requests — end-to-end wire-up did not route via the registered api_base")
	}
}
