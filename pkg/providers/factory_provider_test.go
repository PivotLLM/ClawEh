// ClawEh - Personal AI Assistant
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package providers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/config"
)

func TestCreateProviderFromConfig_OpenAI(t *testing.T) {
	model := &config.ModelConfig{
		ModelName: "test-openai",
		Model:     "gpt-4o",
		Provider:  "openai",
	}
	prov := &config.Provider{
		Name:     "openai",
		Protocol: "openai-chat",
		BaseURL:  "https://api.example.com/v1",
		APIKey:   "test-key",
	}

	provider, modelID, err := CreateProviderFromConfig(model, prov)
	if err != nil {
		t.Fatalf("CreateProviderFromConfig() error = %v", err)
	}
	if provider == nil {
		t.Fatal("CreateProviderFromConfig() returned nil provider")
	}
	if _, ok := provider.(*HTTPProvider); !ok {
		t.Fatalf("expected *HTTPProvider, got %T", provider)
	}
	if modelID != "gpt-4o" {
		t.Errorf("modelID = %q, want %q", modelID, "gpt-4o")
	}
}

func TestCreateProviderFromConfig_OpenAIHTTPProviderForVariousEndpoints(t *testing.T) {
	// All of these are reached via the openai wire protocol; only the
	// endpoint base_url differs. The factory builds an *HTTPProvider for each.
	bases := []struct {
		name    string
		baseURL string
	}{
		{"openai", "https://api.openai.com/v1"},
		{"groq", "https://api.groq.com/openai/v1"},
		{"openrouter", "https://openrouter.ai/api/v1"},
		{"cerebras", "https://api.cerebras.ai/v1"},
		{"deepseek", "https://api.deepseek.com/v1"},
		{"ollama", "http://localhost:11434/v1"},
	}

	for _, tt := range bases {
		t.Run(tt.name, func(t *testing.T) {
			model := &config.ModelConfig{
				ModelName: "test-" + tt.name,
				Model:     "test-model",
				Provider:  tt.name,
			}
			prov := &config.Provider{
				Name:     tt.name,
				Protocol: "openai-chat",
				BaseURL:  tt.baseURL,
				APIKey:   "test-key",
			}

			provider, _, err := CreateProviderFromConfig(model, prov)
			if err != nil {
				t.Fatalf("CreateProviderFromConfig() error = %v", err)
			}
			if _, ok := provider.(*HTTPProvider); !ok {
				t.Fatalf("expected *HTTPProvider, got %T", provider)
			}
		})
	}
}

func TestCreateProviderFromConfig_RawModelIDWithSlash(t *testing.T) {
	// A raw model id may itself contain a slash (e.g. an OpenRouter upstream
	// id). It is used verbatim — never re-parsed for a protocol prefix.
	model := &config.ModelConfig{
		ModelName: "test-nvidia",
		Model:     "meta/llama-3.1-8b",
		Provider:  "nvidia",
	}
	prov := &config.Provider{
		Name:     "nvidia",
		Protocol: "openai-chat",
		BaseURL:  "https://integrate.api.nvidia.com/v1",
		APIKey:   "nvapi-test",
	}

	_, modelID, err := CreateProviderFromConfig(model, prov)
	if err != nil {
		t.Fatalf("CreateProviderFromConfig() error = %v", err)
	}
	if modelID != "meta/llama-3.1-8b" {
		t.Errorf("modelID = %q, want meta/llama-3.1-8b", modelID)
	}
}

func TestCreateProviderFromConfig_Anthropic(t *testing.T) {
	model := &config.ModelConfig{
		ModelName: "test-anthropic",
		Model:     "claude-sonnet-4.6",
		Provider:  "anthropic",
	}
	prov := &config.Provider{
		Name:     "anthropic",
		Protocol: "anthropic",
		BaseURL:  "https://api.anthropic.com/v1",
		APIKey:   "test-key",
	}

	provider, modelID, err := CreateProviderFromConfig(model, prov)
	if err != nil {
		t.Fatalf("CreateProviderFromConfig() error = %v", err)
	}
	if provider == nil {
		t.Fatal("CreateProviderFromConfig() returned nil provider")
	}
	if modelID != "claude-sonnet-4.6" {
		t.Errorf("modelID = %q, want %q", modelID, "claude-sonnet-4.6")
	}
}

func TestCreateProviderFromConfig_ClaudeCLI(t *testing.T) {
	model := &config.ModelConfig{
		ModelName: "test-claude-cli",
		Model:     "claude-sonnet-4.6",
		Provider:  "claude-cli",
	}
	prov := &config.Provider{Name: "claude-cli", Protocol: "claude-cli"}

	provider, modelID, err := CreateProviderFromConfig(model, prov)
	if err != nil {
		t.Fatalf("CreateProviderFromConfig() error = %v", err)
	}
	if _, ok := provider.(*ClaudeCliProvider); !ok {
		t.Fatalf("expected *ClaudeCliProvider, got %T", provider)
	}
	if modelID != "claude-sonnet-4.6" {
		t.Errorf("modelID = %q, want %q", modelID, "claude-sonnet-4.6")
	}
}

func TestCreateProviderFromConfig_CodexCLI(t *testing.T) {
	model := &config.ModelConfig{
		ModelName: "test-codex-cli",
		Model:     "codex",
		Provider:  "codex-cli",
	}
	prov := &config.Provider{Name: "codex-cli", Protocol: "codex-cli"}

	provider, modelID, err := CreateProviderFromConfig(model, prov)
	if err != nil {
		t.Fatalf("CreateProviderFromConfig() error = %v", err)
	}
	if _, ok := provider.(*CodexCliProvider); !ok {
		t.Fatalf("expected *CodexCliProvider, got %T", provider)
	}
	if modelID != "codex" {
		t.Errorf("modelID = %q, want %q", modelID, "codex")
	}
}

func TestCreateProviderFromConfig_GeminiCLI(t *testing.T) {
	model := &config.ModelConfig{
		ModelName: "test-gemini-cli",
		Model:     "gemini-2.5-flash",
		Provider:  "gemini-cli",
	}
	prov := &config.Provider{Name: "gemini-cli", Protocol: "gemini-cli"}

	provider, modelID, err := CreateProviderFromConfig(model, prov)
	if err != nil {
		t.Fatalf("CreateProviderFromConfig() error = %v", err)
	}
	if _, ok := provider.(*GeminiCliProvider); !ok {
		t.Fatalf("expected *GeminiCliProvider, got %T", provider)
	}
	if modelID != "gemini-2.5-flash" {
		t.Errorf("modelID = %q, want %q", modelID, "gemini-2.5-flash")
	}
}

func TestCreateProviderFromConfig_AnthropicMissingAPIKey(t *testing.T) {
	model := &config.ModelConfig{
		ModelName: "test-no-key",
		Model:     "claude-sonnet-4.6",
		Provider:  "anthropic",
	}
	prov := &config.Provider{
		Name:     "anthropic",
		Protocol: "anthropic",
		BaseURL:  "https://api.anthropic.com/v1",
	}

	if _, _, err := CreateProviderFromConfig(model, prov); err == nil {
		t.Fatal("CreateProviderFromConfig() expected error for missing API key on anthropic protocol")
	}
}

func TestCreateProviderFromConfig_UnknownProtocol(t *testing.T) {
	model := &config.ModelConfig{
		ModelName: "test-unknown",
		Model:     "model",
		Provider:  "weird",
	}
	prov := &config.Provider{Name: "weird", Protocol: "unknown-protocol"}

	if _, _, err := CreateProviderFromConfig(model, prov); err == nil {
		t.Fatal("CreateProviderFromConfig() expected error for unknown protocol")
	}
}

func TestCreateProviderFromConfig_NilModel(t *testing.T) {
	prov := &config.Provider{Name: "openai", Protocol: "openai-chat", BaseURL: "https://x/v1", APIKey: "k"}
	if _, _, err := CreateProviderFromConfig(nil, prov); err == nil {
		t.Fatal("CreateProviderFromConfig(nil model) expected error")
	}
}

func TestCreateProviderFromConfig_NilProvider(t *testing.T) {
	model := &config.ModelConfig{ModelName: "m", Model: "gpt-4o", Provider: "openai"}
	if _, _, err := CreateProviderFromConfig(model, nil); err == nil {
		t.Fatal("CreateProviderFromConfig(nil provider) expected error")
	}
}

func TestCreateProviderFromConfig_EmptyModel(t *testing.T) {
	model := &config.ModelConfig{ModelName: "test-empty", Model: ""}
	prov := &config.Provider{Name: "openai", Protocol: "openai-chat", BaseURL: "https://x/v1", APIKey: "k"}
	if _, _, err := CreateProviderFromConfig(model, prov); err == nil {
		t.Fatal("CreateProviderFromConfig() expected error for empty model")
	}
}

func TestCreateProviderFromConfig_RequestTimeoutPropagation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(1500 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	model := &config.ModelConfig{
		ModelName:      "test-timeout",
		Model:          "gpt-4o",
		Provider:       "openai",
		RequestTimeout: 1,
	}
	prov := &config.Provider{
		Name:     "openai",
		Protocol: "openai-chat",
		BaseURL:  server.URL,
		APIKey:   "k",
	}

	provider, modelID, err := CreateProviderFromConfig(model, prov)
	if err != nil {
		t.Fatalf("CreateProviderFromConfig() error = %v", err)
	}
	if modelID != "gpt-4o" {
		t.Fatalf("modelID = %q, want %q", modelID, "gpt-4o")
	}

	_, err = provider.Chat(
		t.Context(),
		[]Message{{Role: "user", Content: "hi"}},
		nil,
		modelID,
		nil,
	)
	if err == nil {
		t.Fatal("Chat() expected timeout error, got nil")
	}
	errMsg := err.Error()
	if !strings.Contains(errMsg, "context deadline exceeded") && !strings.Contains(errMsg, "Client.Timeout exceeded") {
		t.Fatalf("Chat() error = %q, want timeout-related error", errMsg)
	}
}

func TestCreateProviderFromConfig_Azure(t *testing.T) {
	model := &config.ModelConfig{
		ModelName: "azure-gpt5",
		Model:     "my-gpt5-deployment",
		Provider:  "azure",
	}
	prov := &config.Provider{
		Name:     "azure",
		Protocol: "azure",
		BaseURL:  "https://my-resource.openai.azure.com",
		APIKey:   "test-azure-key",
	}

	provider, modelID, err := CreateProviderFromConfig(model, prov)
	if err != nil {
		t.Fatalf("CreateProviderFromConfig() error = %v", err)
	}
	if provider == nil {
		t.Fatal("CreateProviderFromConfig() returned nil provider")
	}
	if modelID != "my-gpt5-deployment" {
		t.Errorf("modelID = %q, want %q", modelID, "my-gpt5-deployment")
	}
}
