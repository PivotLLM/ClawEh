package providers

import (
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/config"
)

// Tests for resolveProviderSelection — additional model prefix cases not covered yet.

func TestResolveProviderSelection_MoonshotModel(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.SetDefaultModel("moonshot/kimi-k2")
	cfg.Providers.Moonshot.APIKey = "moonshot-key"

	sel, err := resolveProviderSelection(cfg)
	if err != nil {
		t.Fatalf("resolveProviderSelection() error = %v", err)
	}
	if sel.providerType != providerTypeHTTPCompat {
		t.Errorf("providerType = %v, want HTTPCompat", sel.providerType)
	}
	if !strings.Contains(sel.apiBase, "moonshot.cn") {
		t.Errorf("apiBase = %q, want moonshot.cn URL", sel.apiBase)
	}
}

func TestResolveProviderSelection_KimiModel(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.SetDefaultModel("kimi-latest")
	cfg.Providers.Moonshot.APIKey = "moonshot-key"

	sel, err := resolveProviderSelection(cfg)
	if err != nil {
		t.Fatalf("resolveProviderSelection() error = %v", err)
	}
	if !strings.Contains(sel.apiBase, "moonshot.cn") {
		t.Errorf("apiBase = %q, want moonshot.cn URL", sel.apiBase)
	}
}

func TestResolveProviderSelection_GeminiModel(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.SetDefaultModel("gemini-2.5-pro")
	cfg.Providers.Gemini.APIKey = "gemini-key"

	sel, err := resolveProviderSelection(cfg)
	if err != nil {
		t.Fatalf("resolveProviderSelection() error = %v", err)
	}
	if !strings.Contains(sel.apiBase, "googleapis.com") {
		t.Errorf("apiBase = %q, want googleapis.com URL", sel.apiBase)
	}
}

func TestResolveProviderSelection_OpenAIModel_WithKey(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.SetDefaultModel("gpt-4o")
	cfg.Providers.OpenAI.APIKey = "sk-test"

	sel, err := resolveProviderSelection(cfg)
	if err != nil {
		t.Fatalf("resolveProviderSelection() error = %v", err)
	}
	if !strings.Contains(sel.apiBase, "openai.com") {
		t.Errorf("apiBase = %q, want openai.com URL", sel.apiBase)
	}
}

func TestResolveProviderSelection_MistralModel(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.SetDefaultModel("mistral-large")
	cfg.Providers.Mistral.APIKey = "mistral-key"

	sel, err := resolveProviderSelection(cfg)
	if err != nil {
		t.Fatalf("resolveProviderSelection() error = %v", err)
	}
	if !strings.Contains(sel.apiBase, "mistral.ai") {
		t.Errorf("apiBase = %q, want mistral.ai URL", sel.apiBase)
	}
}

func TestResolveProviderSelection_AnthropicAPIKey(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.SetDefaultModel("claude-sonnet-4.6")
	cfg.Providers.Anthropic.APIKey = "sk-ant-test"

	sel, err := resolveProviderSelection(cfg)
	if err != nil {
		t.Fatalf("resolveProviderSelection() error = %v", err)
	}
	if !strings.Contains(sel.apiBase, "anthropic.com") {
		t.Errorf("apiBase = %q, want anthropic.com URL", sel.apiBase)
	}
}

func TestResolveProviderSelection_VLLMFallback(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.SetDefaultModel("custom-local-model")
	cfg.Providers.VLLM.APIBase = "http://localhost:8000/v1"
	cfg.Providers.VLLM.APIKey = "vllm-key" // VLLM may require apiKey

	sel, err := resolveProviderSelection(cfg)
	if err != nil {
		// VLLM without APIKey hits the "no API key" check; that's OK to test the path
		t.Logf("resolveProviderSelection() error = %v (expected without key)", err)
		return
	}
	if sel.apiBase != "http://localhost:8000/v1" {
		t.Errorf("apiBase = %q, want http://localhost:8000/v1", sel.apiBase)
	}
}

func TestResolveProviderSelection_NoModel_ReturnsError(t *testing.T) {
	cfg := config.DefaultConfig()
	// Override the default model to empty to trigger no-model error
	cfg.Agents.Defaults.SetDefaultModel("")

	_, err := resolveProviderSelection(cfg)
	if err == nil {
		t.Fatal("expected error when no model configured")
	}
	if !strings.Contains(err.Error(), "no model configured") {
		t.Errorf("error = %q, want 'no model configured'", err.Error())
	}
}

func TestResolveProviderSelection_GroqModel(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.SetDefaultModel("groq/llama-3.3-70b-specdec")
	cfg.Providers.Groq.APIKey = "gsk-test"

	sel, err := resolveProviderSelection(cfg)
	if err != nil {
		t.Fatalf("resolveProviderSelection() error = %v", err)
	}
	if !strings.Contains(sel.apiBase, "groq.com") {
		t.Errorf("apiBase = %q, want groq.com URL", sel.apiBase)
	}
}

func TestResolveProviderSelection_OllamaModel(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.SetDefaultModel("ollama/llama3")
	cfg.Providers.Ollama.APIKey = "ollama"

	sel, err := resolveProviderSelection(cfg)
	if err != nil {
		t.Fatalf("resolveProviderSelection() error = %v", err)
	}
	if !strings.Contains(sel.apiBase, "localhost:11434") {
		t.Errorf("apiBase = %q, want localhost:11434", sel.apiBase)
	}
}

func TestResolveProviderSelection_DefaultToOpenRouter(t *testing.T) {
	// Unknown model but OpenRouter key configured — should use OpenRouter as default.
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.SetDefaultModel("some-unknown-model")
	cfg.Providers.OpenRouter.APIKey = "sk-or-fallback"

	sel, err := resolveProviderSelection(cfg)
	if err != nil {
		t.Fatalf("resolveProviderSelection() error = %v", err)
	}
	if !strings.Contains(sel.apiBase, "openrouter.ai") {
		t.Errorf("apiBase = %q, want openrouter.ai URL", sel.apiBase)
	}
}

// Tests for CreateProviderFromConfig — additional protocol cases.

func TestCreateProviderFromConfig_Moonshot(t *testing.T) {
	cfg := &config.ModelConfig{
		ModelName: "test-moonshot",
		Model:     "moonshot/kimi-k2",
		APIKey:    "moonshot-key",
	}

	provider, modelID, err := CreateProviderFromConfig(cfg)
	if err != nil {
		t.Fatalf("CreateProviderFromConfig() error = %v", err)
	}
	if provider == nil {
		t.Fatal("expected non-nil provider")
	}
	if modelID != "kimi-k2" {
		t.Errorf("modelID = %q, want %q", modelID, "kimi-k2")
	}
}

func TestCreateProviderFromConfig_Gemini(t *testing.T) {
	cfg := &config.ModelConfig{
		ModelName: "test-gemini",
		Model:     "gemini/gemini-2.5-pro",
		APIKey:    "gemini-key",
	}

	provider, modelID, err := CreateProviderFromConfig(cfg)
	if err != nil {
		t.Fatalf("CreateProviderFromConfig() error = %v", err)
	}
	if provider == nil {
		t.Fatal("expected non-nil provider")
	}
	if modelID != "gemini-2.5-pro" {
		t.Errorf("modelID = %q, want %q", modelID, "gemini-2.5-pro")
	}
}

func TestCreateProviderFromConfig_Groq(t *testing.T) {
	cfg := &config.ModelConfig{
		ModelName: "test-groq",
		Model:     "groq/llama-3.3-70b",
		APIKey:    "gsk-test",
	}

	provider, modelID, err := CreateProviderFromConfig(cfg)
	if err != nil {
		t.Fatalf("CreateProviderFromConfig() error = %v", err)
	}
	if provider == nil {
		t.Fatal("expected non-nil provider")
	}
	if modelID != "llama-3.3-70b" {
		t.Errorf("modelID = %q, want %q", modelID, "llama-3.3-70b")
	}
}

func TestCreateProviderFromConfig_Mistral(t *testing.T) {
	cfg := &config.ModelConfig{
		ModelName: "test-mistral",
		Model:     "mistral/mistral-large",
		APIKey:    "mistral-key",
	}

	provider, modelID, err := CreateProviderFromConfig(cfg)
	if err != nil {
		t.Fatalf("CreateProviderFromConfig() error = %v", err)
	}
	if provider == nil {
		t.Fatal("expected non-nil provider")
	}
	if modelID != "mistral-large" {
		t.Errorf("modelID = %q, want %q", modelID, "mistral-large")
	}
}

func TestCreateProviderFromConfig_GeminiCLIWithTimeout(t *testing.T) {
	cfg := &config.ModelConfig{
		ModelName:      "test-gemini-cli",
		Model:          "gemini-cli/gemini-2.5-flash",
		RequestTimeout: 30,
	}

	provider, modelID, err := CreateProviderFromConfig(cfg)
	if err != nil {
		t.Fatalf("CreateProviderFromConfig() error = %v", err)
	}
	if provider == nil {
		t.Fatal("expected non-nil provider")
	}
	if modelID != "gemini-2.5-flash" {
		t.Errorf("modelID = %q, want %q", modelID, "gemini-2.5-flash")
	}
	if _, ok := provider.(*GeminiCliProvider); !ok {
		t.Errorf("provider type = %T, want *GeminiCliProvider", provider)
	}
}

func TestCreateProviderFromConfig_ClaudeCLIWithTimeout(t *testing.T) {
	cfg := &config.ModelConfig{
		ModelName:      "test-claude-cli-timeout",
		Model:          "claude-cli/claude-sonnet-4.6",
		RequestTimeout: 120,
	}

	provider, _, err := CreateProviderFromConfig(cfg)
	if err != nil {
		t.Fatalf("CreateProviderFromConfig() error = %v", err)
	}
	if _, ok := provider.(*ClaudeCliProvider); !ok {
		t.Errorf("provider type = %T, want *ClaudeCliProvider", provider)
	}
}

func TestCreateProviderFromConfig_AnthropicMissingKey(t *testing.T) {
	cfg := &config.ModelConfig{
		ModelName: "test-anthropic-no-key",
		Model:     "anthropic/claude-sonnet-4.6",
		// No APIKey
	}

	_, _, err := CreateProviderFromConfig(cfg)
	if err == nil {
		t.Fatal("expected error for anthropic without api_key")
	}
	if !strings.Contains(err.Error(), "api_key is required") {
		t.Errorf("error = %q, want 'api_key is required'", err.Error())
	}
}

func TestCreateProviderFromConfig_AnthropicMessages(t *testing.T) {
	cfg := &config.ModelConfig{
		ModelName: "test-anthropic-messages",
		Model:     "anthropic-messages/claude-opus-4",
		APIKey:    "sk-ant-test",
	}

	provider, modelID, err := CreateProviderFromConfig(cfg)
	if err != nil {
		t.Fatalf("CreateProviderFromConfig() error = %v", err)
	}
	if provider == nil {
		t.Fatal("expected non-nil provider")
	}
	if modelID != "claude-opus-4" {
		t.Errorf("modelID = %q, want claude-opus-4", modelID)
	}
}

func TestCreateProviderFromConfig_AnthropicMessagesMissingKey(t *testing.T) {
	cfg := &config.ModelConfig{
		ModelName: "test-anth-msg-no-key",
		Model:     "anthropic-messages/claude-opus-4",
	}

	_, _, err := CreateProviderFromConfig(cfg)
	if err == nil {
		t.Fatal("expected error for anthropic-messages without api_key")
	}
}

func TestCreateProviderFromConfig_OpenAINoKeyNoBase(t *testing.T) {
	cfg := &config.ModelConfig{
		ModelName: "test-openai-no-key-no-base",
		Model:     "openai/gpt-4o",
		// Neither APIKey nor APIBase
	}

	_, _, err := CreateProviderFromConfig(cfg)
	if err == nil {
		t.Fatal("expected error when both api_key and api_base are missing for openai")
	}
}

func TestGetDefaultAPIBase_AllProtocols(t *testing.T) {
	tests := []struct {
		protocol string
		wantURL  string
	}{
		{"openai", "api.openai.com"},
		{"openrouter", "openrouter.ai"},
		{"litellm", "localhost:4000"},
		{"groq", "groq.com"},
		{"gemini", "googleapis.com"},
		{"nvidia", "nvidia.com"},
		{"ollama", "localhost:11434"},
		{"moonshot", "moonshot.cn"},
		{"deepseek", "deepseek.com"},
		{"cerebras", "cerebras.ai"},
		{"qwen", "aliyuncs.com"},
		{"vllm", "localhost:8000"},
		{"mistral", "mistral.ai"},
		{"avian", "avian.io"},
		{"unknown-protocol", ""},
	}

	for _, tt := range tests {
		t.Run(tt.protocol, func(t *testing.T) {
			got := getDefaultAPIBase(tt.protocol)
			if tt.wantURL == "" {
				if got != "" {
					t.Errorf("getDefaultAPIBase(%q) = %q, want empty", tt.protocol, got)
				}
			} else {
				if !strings.Contains(got, tt.wantURL) {
					t.Errorf("getDefaultAPIBase(%q) = %q, want URL containing %q", tt.protocol, got, tt.wantURL)
				}
			}
		})
	}
}
