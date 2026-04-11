package providers

import (
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/config"
)

// Additional CreateProviderFromConfig paths not covered by existing tests.

func TestCreateProviderFromConfig_CodexCLIWithTimeout(t *testing.T) {
	cfg := &config.ModelConfig{
		ModelName:      "test-codex-cli-timeout",
		Model:          "codex-cli/codex",
		RequestTimeout: 60,
	}

	provider, modelID, err := CreateProviderFromConfig(cfg)
	if err != nil {
		t.Fatalf("CreateProviderFromConfig() error = %v", err)
	}
	if provider == nil {
		t.Fatal("expected non-nil provider")
	}
	if modelID != "codex" {
		t.Errorf("modelID = %q, want codex", modelID)
	}
	if _, ok := provider.(*CodexCliProvider); !ok {
		t.Errorf("provider type = %T, want *CodexCliProvider", provider)
	}
}

func TestCreateProviderFromConfig_CodexCLIAlias(t *testing.T) {
	cfg := &config.ModelConfig{
		ModelName: "test-codexcli",
		Model:     "codexcli/codex",
	}

	provider, modelID, err := CreateProviderFromConfig(cfg)
	if err != nil {
		t.Fatalf("CreateProviderFromConfig() error = %v", err)
	}
	if modelID != "codex" {
		t.Errorf("modelID = %q, want codex", modelID)
	}
	if _, ok := provider.(*CodexCliProvider); !ok {
		t.Errorf("provider type = %T, want *CodexCliProvider", provider)
	}
}

func TestCreateProviderFromConfig_ClaudeCLIAlias(t *testing.T) {
	cfg := &config.ModelConfig{
		ModelName: "test-claudecli",
		Model:     "claudecli/claude-sonnet-4.6",
	}

	provider, modelID, err := CreateProviderFromConfig(cfg)
	if err != nil {
		t.Fatalf("CreateProviderFromConfig() error = %v", err)
	}
	if modelID != "claude-sonnet-4.6" {
		t.Errorf("modelID = %q, want claude-sonnet-4.6", modelID)
	}
	if _, ok := provider.(*ClaudeCliProvider); !ok {
		t.Errorf("provider type = %T, want *ClaudeCliProvider", provider)
	}
}

func TestCreateProviderFromConfig_GeminiCLIAlias(t *testing.T) {
	cfg := &config.ModelConfig{
		ModelName: "test-geminicli",
		Model:     "geminicli/gemini-2.5-flash",
	}

	provider, modelID, err := CreateProviderFromConfig(cfg)
	if err != nil {
		t.Fatalf("CreateProviderFromConfig() error = %v", err)
	}
	if modelID != "gemini-2.5-flash" {
		t.Errorf("modelID = %q, want gemini-2.5-flash", modelID)
	}
	if _, ok := provider.(*GeminiCliProvider); !ok {
		t.Errorf("provider type = %T, want *GeminiCliProvider", provider)
	}
}

func TestCreateProviderFromConfig_Bedrock_RegionFromAPIBase(t *testing.T) {
	cfg := &config.ModelConfig{
		ModelName: "test-bedrock-region",
		Model:     "bedrock/anthropic.claude-3-5-sonnet-20241022-v2:0",
		APIBase:   "us-east-1", // region name (no "://")
	}

	// Bedrock initialization may fail without real AWS credentials, but must not panic.
	provider, modelID, err := CreateProviderFromConfig(cfg)
	if err != nil {
		// Expected without real AWS credentials.
		t.Logf("bedrock provider error (expected without credentials): %v", err)
		return
	}
	if modelID != "anthropic.claude-3-5-sonnet-20241022-v2:0" {
		t.Errorf("modelID = %q", modelID)
	}
	_ = provider
}

func TestCreateProviderFromConfig_Bedrock_StaticCredentials(t *testing.T) {
	cfg := &config.ModelConfig{
		ModelName: "test-bedrock-static",
		Model:     "bedrock/anthropic.claude-3-sonnet",
		APIKey:    "ACCESS_KEY:SECRET_KEY",
		APIBase:   "us-west-2",
	}

	provider, modelID, err := CreateProviderFromConfig(cfg)
	if err != nil {
		t.Logf("bedrock provider error (expected without real credentials): %v", err)
		return
	}
	if modelID != "anthropic.claude-3-sonnet" {
		t.Errorf("modelID = %q", modelID)
	}
	_ = provider
}

func TestCreateProviderFromConfig_Bedrock_BearerToken(t *testing.T) {
	cfg := &config.ModelConfig{
		ModelName: "test-bedrock-bearer",
		Model:     "bedrock/amazon.nova-pro-v1:0",
		APIKey:    "bak-abc123def456", // no colon = bearer token
	}

	provider, modelID, err := CreateProviderFromConfig(cfg)
	if err != nil {
		t.Logf("bedrock provider error (expected without real credentials): %v", err)
		return
	}
	if modelID != "amazon.nova-pro-v1:0" {
		t.Errorf("modelID = %q", modelID)
	}
	_ = provider
}

func TestCreateProviderFromConfig_Bedrock_WithTimeout(t *testing.T) {
	cfg := &config.ModelConfig{
		ModelName:      "test-bedrock-timeout",
		Model:          "bedrock/anthropic.claude-3-sonnet",
		APIBase:        "us-east-1",
		RequestTimeout: 30,
	}

	provider, modelID, err := CreateProviderFromConfig(cfg)
	if err != nil {
		t.Logf("bedrock provider error (expected without real credentials): %v", err)
		return
	}
	if modelID != "anthropic.claude-3-sonnet" {
		t.Errorf("modelID = %q", modelID)
	}
	_ = provider
}

func TestCreateProviderFromConfig_Deepseek(t *testing.T) {
	cfg := &config.ModelConfig{
		ModelName: "test-deepseek",
		Model:     "deepseek/deepseek-chat",
		APIKey:    "ds-test-key",
	}

	provider, modelID, err := CreateProviderFromConfig(cfg)
	if err != nil {
		t.Fatalf("CreateProviderFromConfig() error = %v", err)
	}
	if provider == nil {
		t.Fatal("expected non-nil provider")
	}
	if modelID != "deepseek-chat" {
		t.Errorf("modelID = %q, want deepseek-chat", modelID)
	}
}

func TestCreateProviderFromConfig_Cerebras(t *testing.T) {
	cfg := &config.ModelConfig{
		ModelName: "test-cerebras",
		Model:     "cerebras/llama3.1-70b",
		APIKey:    "csk-test",
	}

	provider, modelID, err := CreateProviderFromConfig(cfg)
	if err != nil {
		t.Fatalf("CreateProviderFromConfig() error = %v", err)
	}
	if modelID != "llama3.1-70b" {
		t.Errorf("modelID = %q, want llama3.1-70b", modelID)
	}
	_ = provider
}

func TestCreateProviderFromConfig_Qwen(t *testing.T) {
	cfg := &config.ModelConfig{
		ModelName: "test-qwen",
		Model:     "qwen/qwen-plus",
		APIKey:    "qwen-test-key",
	}

	provider, modelID, err := CreateProviderFromConfig(cfg)
	if err != nil {
		t.Fatalf("CreateProviderFromConfig() error = %v", err)
	}
	if modelID != "qwen-plus" {
		t.Errorf("modelID = %q, want qwen-plus", modelID)
	}
	_ = provider
}

func TestCreateProviderFromConfig_Avian(t *testing.T) {
	cfg := &config.ModelConfig{
		ModelName: "test-avian",
		Model:     "avian/gpt-4o",
		APIKey:    "avian-test-key",
	}

	provider, modelID, err := CreateProviderFromConfig(cfg)
	if err != nil {
		t.Fatalf("CreateProviderFromConfig() error = %v", err)
	}
	if modelID != "gpt-4o" {
		t.Errorf("modelID = %q, want gpt-4o", modelID)
	}
	_ = provider
}

func TestCreateProviderFromConfig_Nvidia(t *testing.T) {
	cfg := &config.ModelConfig{
		ModelName: "test-nvidia",
		Model:     "nvidia/meta/llama-3.1-70b-instruct",
		APIKey:    "nvidia-test-key",
	}

	provider, modelID, err := CreateProviderFromConfig(cfg)
	if err != nil {
		t.Fatalf("CreateProviderFromConfig() error = %v", err)
	}
	if modelID != "meta/llama-3.1-70b-instruct" {
		t.Errorf("modelID = %q, want meta/llama-3.1-70b-instruct", modelID)
	}
	_ = provider
}

func TestCreateProviderFromConfig_AnthropicWithCustomBase(t *testing.T) {
	cfg := &config.ModelConfig{
		ModelName: "test-anthropic-custom-base",
		Model:     "anthropic/claude-opus-4",
		APIKey:    "sk-ant-test",
		APIBase:   "https://my-proxy.example.com/v1",
	}

	provider, modelID, err := CreateProviderFromConfig(cfg)
	if err != nil {
		t.Fatalf("CreateProviderFromConfig() error = %v", err)
	}
	if modelID != "claude-opus-4" {
		t.Errorf("modelID = %q, want claude-opus-4", modelID)
	}
	_ = provider
}
