// PicoClaw - Ultra-lightweight personal AI agent
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package config

import (
	"strings"
	"testing"
)

func TestConvertProvidersToModelList_OpenAI(t *testing.T) {
	cfg := &Config{
		Providers: ProvidersConfig{
			OpenAI: OpenAIProviderConfig{
				ProviderConfig: ProviderConfig{
					APIKey:  "sk-test-key",
					APIBase: "https://custom.api.com/v1",
				},
			},
		},
	}

	result := ConvertProvidersToModelList(cfg)

	if len(result) != 1 {
		t.Fatalf("len(result) = %d, want 1", len(result))
	}

	if result[0].ModelName != "openai" {
		t.Errorf("ModelName = %q, want %q", result[0].ModelName, "openai")
	}
	if result[0].Model != "openai/gpt-5.4" {
		t.Errorf("Model = %q, want %q", result[0].Model, "openai/gpt-5.4")
	}
	if result[0].APIKey != "sk-test-key" {
		t.Errorf("APIKey = %q, want %q", result[0].APIKey, "sk-test-key")
	}
}

func TestConvertProvidersToModelList_Anthropic(t *testing.T) {
	cfg := &Config{
		Providers: ProvidersConfig{
			Anthropic: ProviderConfig{
				APIKey:  "ant-key",
				APIBase: "https://custom.anthropic.com",
			},
		},
	}

	result := ConvertProvidersToModelList(cfg)

	if len(result) != 1 {
		t.Fatalf("len(result) = %d, want 1", len(result))
	}

	if result[0].ModelName != "anthropic" {
		t.Errorf("ModelName = %q, want %q", result[0].ModelName, "anthropic")
	}
	if result[0].Model != "anthropic/claude-sonnet-4.6" {
		t.Errorf("Model = %q, want %q", result[0].Model, "anthropic/claude-sonnet-4.6")
	}
}

func TestConvertProvidersToModelList_LiteLLM(t *testing.T) {
	cfg := &Config{
		Providers: ProvidersConfig{
			LiteLLM: ProviderConfig{
				APIKey:  "litellm-key",
				APIBase: "http://localhost:4000/v1",
			},
		},
	}

	result := ConvertProvidersToModelList(cfg)

	if len(result) != 1 {
		t.Fatalf("len(result) = %d, want 1", len(result))
	}

	if result[0].ModelName != "litellm" {
		t.Errorf("ModelName = %q, want %q", result[0].ModelName, "litellm")
	}
	if result[0].Model != "litellm/auto" {
		t.Errorf("Model = %q, want %q", result[0].Model, "litellm/auto")
	}
	if result[0].APIBase != "http://localhost:4000/v1" {
		t.Errorf("APIBase = %q, want %q", result[0].APIBase, "http://localhost:4000/v1")
	}
}

func TestConvertProvidersToModelList_Multiple(t *testing.T) {
	cfg := &Config{
		Providers: ProvidersConfig{
			OpenAI:   OpenAIProviderConfig{ProviderConfig: ProviderConfig{APIKey: "openai-key"}},
			Groq:     ProviderConfig{APIKey: "groq-key"},
			DeepSeek: ProviderConfig{APIKey: "deepseek-key"},
		},
	}

	result := ConvertProvidersToModelList(cfg)

	if len(result) != 3 {
		t.Fatalf("len(result) = %d, want 3", len(result))
	}

	// Check that all providers are present
	found := make(map[string]bool)
	for _, mc := range result {
		found[mc.ModelName] = true
	}

	for _, name := range []string{"openai", "groq", "deepseek"} {
		if !found[name] {
			t.Errorf("Missing provider %q in result", name)
		}
	}
}

func TestConvertProvidersToModelList_Empty(t *testing.T) {
	cfg := &Config{
		Providers: ProvidersConfig{},
	}

	result := ConvertProvidersToModelList(cfg)

	if len(result) != 0 {
		t.Errorf("len(result) = %d, want 0", len(result))
	}
}

func TestConvertProvidersToModelList_Nil(t *testing.T) {
	result := ConvertProvidersToModelList(nil)

	if result != nil {
		t.Errorf("result = %v, want nil", result)
	}
}

func TestConvertProvidersToModelList_AllProviders(t *testing.T) {
	cfg := &Config{
		Providers: ProvidersConfig{
			OpenAI:     OpenAIProviderConfig{ProviderConfig: ProviderConfig{APIKey: "key1"}},
			LiteLLM:    ProviderConfig{APIKey: "key-litellm", APIBase: "http://localhost:4000/v1"},
			Anthropic:  ProviderConfig{APIKey: "key2"},
			OpenRouter: ProviderConfig{APIKey: "key3"},
			Groq:       ProviderConfig{APIKey: "key4"},
			VLLM:       ProviderConfig{APIKey: "key6"},
			Gemini:     ProviderConfig{APIKey: "key7"},
			Nvidia:     ProviderConfig{APIKey: "key8"},
			Ollama:     ProviderConfig{APIKey: "key9"},
			Moonshot:   ProviderConfig{APIKey: "key10"},
			DeepSeek:   ProviderConfig{APIKey: "key12"},
			Cerebras:   ProviderConfig{APIKey: "key13"},
			Qwen:       ProviderConfig{APIKey: "key17"},
			Mistral:    ProviderConfig{APIKey: "key18"},
			Avian:      ProviderConfig{APIKey: "key19"},
		},
	}

	result := ConvertProvidersToModelList(cfg)

	// All 15 providers should be converted
	if len(result) != 15 {
		t.Errorf("len(result) = %d, want 15", len(result))
	}
}

func TestConvertProvidersToModelList_Proxy(t *testing.T) {
	cfg := &Config{
		Providers: ProvidersConfig{
			OpenAI: OpenAIProviderConfig{
				ProviderConfig: ProviderConfig{
					APIKey: "key",
					Proxy:  "http://proxy:8080",
				},
			},
		},
	}

	result := ConvertProvidersToModelList(cfg)

	if len(result) != 1 {
		t.Fatalf("len(result) = %d, want 1", len(result))
	}

	if result[0].Proxy != "http://proxy:8080" {
		t.Errorf("Proxy = %q, want %q", result[0].Proxy, "http://proxy:8080")
	}
}

func TestConvertProvidersToModelList_RequestTimeout(t *testing.T) {
	cfg := &Config{
		Providers: ProvidersConfig{
			Ollama: ProviderConfig{
				APIKey:         "ollama-key",
				RequestTimeout: 300,
			},
		},
	}

	result := ConvertProvidersToModelList(cfg)

	if len(result) != 1 {
		t.Fatalf("len(result) = %d, want 1", len(result))
	}

	if result[0].RequestTimeout != 300 {
		t.Errorf("RequestTimeout = %d, want %d", result[0].RequestTimeout, 300)
	}
}

func TestConvertProvidersToModelList_AuthMethod(t *testing.T) {
	cfg := &Config{
		Providers: ProvidersConfig{
			OpenAI: OpenAIProviderConfig{
				ProviderConfig: ProviderConfig{
					AuthMethod: "oauth",
				},
			},
		},
	}

	result := ConvertProvidersToModelList(cfg)

	if len(result) != 0 {
		t.Errorf("len(result) = %d, want 0 (AuthMethod alone should not create entry)", len(result))
	}
}

// Tests for preserving user's configured model during migration

func TestConvertProvidersToModelList_PreservesUserModel_DeepSeek(t *testing.T) {
	cfg := &Config{
		Agents: AgentsConfig{
			Defaults: AgentDefaults{
				Model: &AgentModelConfig{Primary: "deepseek-reasoner"},
			},
		},
		Providers: ProvidersConfig{
			DeepSeek: ProviderConfig{APIKey: "sk-deepseek"},
		},
	}

	result := ConvertProvidersToModelList(cfg)

	if len(result) != 1 {
		t.Fatalf("len(result) = %d, want 1", len(result))
	}

	// Should use user's model, not default
	if result[0].Model != "deepseek/deepseek-reasoner" {
		t.Errorf("Model = %q, want %q (user's configured model)", result[0].Model, "deepseek/deepseek-reasoner")
	}
}

func TestConvertProvidersToModelList_PreservesUserModel_OpenAI(t *testing.T) {
	cfg := &Config{
		Agents: AgentsConfig{
			Defaults: AgentDefaults{
				Model: &AgentModelConfig{Primary: "gpt-4-turbo"},
			},
		},
		Providers: ProvidersConfig{
			OpenAI: OpenAIProviderConfig{ProviderConfig: ProviderConfig{APIKey: "sk-openai"}},
		},
	}

	result := ConvertProvidersToModelList(cfg)

	if len(result) != 1 {
		t.Fatalf("len(result) = %d, want 1", len(result))
	}

	if result[0].Model != "openai/gpt-4-turbo" {
		t.Errorf("Model = %q, want %q", result[0].Model, "openai/gpt-4-turbo")
	}
}

func TestConvertProvidersToModelList_PreservesUserModel_Anthropic(t *testing.T) {
	cfg := &Config{
		Agents: AgentsConfig{
			Defaults: AgentDefaults{
				Model: &AgentModelConfig{Primary: "claude-opus-4-20250514"},
			},
		},
		Providers: ProvidersConfig{
			Anthropic: ProviderConfig{APIKey: "sk-ant"},
		},
	}

	result := ConvertProvidersToModelList(cfg)

	if len(result) != 1 {
		t.Fatalf("len(result) = %d, want 1", len(result))
	}

	if result[0].Model != "anthropic/claude-opus-4-20250514" {
		t.Errorf("Model = %q, want %q", result[0].Model, "anthropic/claude-opus-4-20250514")
	}
}

func TestConvertProvidersToModelList_PreservesUserModel_Qwen(t *testing.T) {
	cfg := &Config{
		Agents: AgentsConfig{
			Defaults: AgentDefaults{
				Model: &AgentModelConfig{Primary: "qwen-plus"},
			},
		},
		Providers: ProvidersConfig{
			Qwen: ProviderConfig{APIKey: "sk-qwen"},
		},
	}

	result := ConvertProvidersToModelList(cfg)

	if len(result) != 1 {
		t.Fatalf("len(result) = %d, want 1", len(result))
	}

	if result[0].Model != "qwen/qwen-plus" {
		t.Errorf("Model = %q, want %q", result[0].Model, "qwen/qwen-plus")
	}
}

func TestConvertProvidersToModelList_UsesDefaultWhenNoUserModel(t *testing.T) {
	cfg := &Config{
		Agents: AgentsConfig{
			Defaults: AgentDefaults{}, // no model specified
		},
		Providers: ProvidersConfig{
			DeepSeek: ProviderConfig{APIKey: "sk-deepseek"},
		},
	}

	result := ConvertProvidersToModelList(cfg)

	if len(result) != 1 {
		t.Fatalf("len(result) = %d, want 1", len(result))
	}

	// Should use default model
	if result[0].Model != "deepseek/deepseek-chat" {
		t.Errorf("Model = %q, want %q (default)", result[0].Model, "deepseek/deepseek-chat")
	}
}

func TestConvertProvidersToModelList_MultipleProviders_UserModelAppliedToFirst(t *testing.T) {
	// When multiple providers are configured and no explicit provider field exists,
	// the user's configured model name is applied to the FIRST provider in migration
	// order (OpenAI), not provider-matched. DeepSeek keeps its default model.
	cfg := &Config{
		Agents: AgentsConfig{
			Defaults: AgentDefaults{
				Model: &AgentModelConfig{Primary: "deepseek-reasoner"},
			},
		},
		Providers: ProvidersConfig{
			OpenAI:   OpenAIProviderConfig{ProviderConfig: ProviderConfig{APIKey: "sk-openai"}},
			DeepSeek: ProviderConfig{APIKey: "sk-deepseek"},
		},
	}

	result := ConvertProvidersToModelList(cfg)

	if len(result) != 2 {
		t.Fatalf("len(result) = %d, want 2", len(result))
	}

	// The first provider (OpenAI) gets the user model applied.
	// ModelName becomes the user model name, Model gets the protocol prefix.
	first := result[0]
	if first.ModelName != "deepseek-reasoner" {
		t.Errorf("first.ModelName = %q, want %q (user's model applied to first provider)", first.ModelName, "deepseek-reasoner")
	}
	if first.Model != "openai/deepseek-reasoner" {
		t.Errorf("first.Model = %q, want %q (openai protocol + user model)", first.Model, "openai/deepseek-reasoner")
	}

	// DeepSeek is the second provider and keeps its default model.
	second := result[1]
	if second.ModelName != "deepseek" {
		t.Errorf("second.ModelName = %q, want %q (default)", second.ModelName, "deepseek")
	}
	if second.Model != "deepseek/deepseek-chat" {
		t.Errorf("second.Model = %q, want %q (default)", second.Model, "deepseek/deepseek-chat")
	}
}

func TestConvertProvidersToModelList_ProviderNameAliases(t *testing.T) {
	tests := []struct {
		providerAlias string
		expectedModel string
		provider      ProviderConfig
	}{
		{"gpt", "openai/gpt-4-custom", ProviderConfig{APIKey: "key"}},
		{"claude", "anthropic/claude-custom", ProviderConfig{APIKey: "key"}},
		{"tongyi", "qwen/qwen-custom", ProviderConfig{APIKey: "key"}},
		{"kimi", "moonshot/kimi-custom", ProviderConfig{APIKey: "key"}},
	}

	for _, tt := range tests {
		t.Run(tt.providerAlias, func(t *testing.T) {
			modelName := strings.TrimPrefix(
				tt.expectedModel,
				tt.expectedModel[:strings.Index(tt.expectedModel, "/")+1],
			)
			cfg := &Config{
				Agents: AgentsConfig{
					Defaults: AgentDefaults{
						Model: &AgentModelConfig{Primary: modelName},
					},
				},
				Providers: ProvidersConfig{},
			}

			// Set the appropriate provider config
			switch tt.providerAlias {
			case "gpt":
				cfg.Providers.OpenAI = OpenAIProviderConfig{ProviderConfig: tt.provider}
			case "claude":
				cfg.Providers.Anthropic = tt.provider
			case "tongyi":
				cfg.Providers.Qwen = tt.provider
			case "kimi":
				cfg.Providers.Moonshot = tt.provider
			}

			result := ConvertProvidersToModelList(cfg)
			if len(result) != 1 {
				t.Fatalf("len(result) = %d, want 1", len(result))
			}

			// Extract just the model ID part (after the first /)
			expectedModelID := tt.expectedModel
			if result[0].Model != expectedModelID {
				t.Errorf("Model = %q, want %q", result[0].Model, expectedModelID)
			}
		})
	}
}

// Test for backward compatibility: single provider without explicit provider field
// This matches the legacy config pattern where users only set model, not provider

func TestConvertProvidersToModelList_NoProviderField_SingleProvider(t *testing.T) {
	// Legacy config: no explicit provider field, only deepseek has API key configured
	cfg := &Config{
		Agents: AgentsConfig{
			Defaults: AgentDefaults{
				Model: &AgentModelConfig{Primary: "deepseek-chat"},
			},
		},
		Providers: ProvidersConfig{
			DeepSeek: ProviderConfig{APIKey: "test-deepseek-key"},
		},
	}

	result := ConvertProvidersToModelList(cfg)

	if len(result) != 1 {
		t.Fatalf("len(result) = %d, want 1", len(result))
	}

	// ModelName should be the user's model value for backward compatibility
	if result[0].ModelName != "deepseek-chat" {
		t.Errorf("ModelName = %q, want %q (user's model for backward compatibility)", result[0].ModelName, "deepseek-chat")
	}

	// Model should use the user's model with protocol prefix
	if result[0].Model != "deepseek/deepseek-chat" {
		t.Errorf("Model = %q, want %q", result[0].Model, "deepseek/deepseek-chat")
	}
}

func TestConvertProvidersToModelList_NoProviderField_MultipleProviders(t *testing.T) {
	// When multiple providers are configured but no provider field is set,
	// the FIRST provider (in migration order) will use userModel as ModelName
	// for backward compatibility with legacy implicit provider selection
	cfg := &Config{
		Agents: AgentsConfig{
			Defaults: AgentDefaults{
				Model: &AgentModelConfig{Primary: "some-model"},
			},
		},
		Providers: ProvidersConfig{
			OpenAI:   OpenAIProviderConfig{ProviderConfig: ProviderConfig{APIKey: "openai-key"}},
			DeepSeek: ProviderConfig{APIKey: "deepseek-key"},
		},
	}

	result := ConvertProvidersToModelList(cfg)

	if len(result) != 2 {
		t.Fatalf("len(result) = %d, want 2", len(result))
	}

	// The first provider (OpenAI in migration order) should use userModel as ModelName
	// This ensures GetModelConfig("some-model") will find it
	if result[0].ModelName != "some-model" {
		t.Errorf("First provider ModelName = %q, want %q", result[0].ModelName, "some-model")
	}

	// Other providers should use provider name as ModelName
	if result[1].ModelName != "deepseek" {
		t.Errorf("Second provider ModelName = %q, want %q", result[1].ModelName, "deepseek")
	}
}

func TestConvertProvidersToModelList_NoProviderField_NoModel(t *testing.T) {
	// Edge case: no model set, single provider configured
	cfg := &Config{
		Agents: AgentsConfig{
			Defaults: AgentDefaults{},
		},
		Providers: ProvidersConfig{
			DeepSeek: ProviderConfig{APIKey: "deepseek-key"},
		},
	}

	result := ConvertProvidersToModelList(cfg)

	if len(result) != 1 {
		t.Fatalf("len(result) = %d, want 1", len(result))
	}

	// Should use default provider name since no model is specified
	if result[0].ModelName != "deepseek" {
		t.Errorf("ModelName = %q, want %q", result[0].ModelName, "deepseek")
	}
}

// Tests for buildModelWithProtocol helper function

func TestBuildModelWithProtocol_NoPrefix(t *testing.T) {
	result := buildModelWithProtocol("openai", "gpt-5.4")
	if result != "openai/gpt-5.4" {
		t.Errorf("buildModelWithProtocol(openai, gpt-5.4) = %q, want %q", result, "openai/gpt-5.4")
	}
}

func TestBuildModelWithProtocol_AlreadyHasPrefix(t *testing.T) {
	result := buildModelWithProtocol("openrouter", "openrouter/auto")
	if result != "openrouter/auto" {
		t.Errorf("buildModelWithProtocol(openrouter, openrouter/auto) = %q, want %q", result, "openrouter/auto")
	}
}

func TestBuildModelWithProtocol_DifferentPrefix(t *testing.T) {
	result := buildModelWithProtocol("anthropic", "openrouter/claude-sonnet-4.6")
	if result != "openrouter/claude-sonnet-4.6" {
		t.Errorf(
			"buildModelWithProtocol(anthropic, openrouter/claude-sonnet-4.6) = %q, want %q",
			result,
			"openrouter/claude-sonnet-4.6",
		)
	}
}

// Test for legacy config with protocol prefix in model name
func TestConvertProvidersToModelList_LegacyModelWithProtocolPrefix(t *testing.T) {
	cfg := &Config{
		Agents: AgentsConfig{
			Defaults: AgentDefaults{
				Model: &AgentModelConfig{Primary: "openrouter/auto"},
			},
		},
		Providers: ProvidersConfig{
			OpenRouter: ProviderConfig{APIKey: "sk-or-test"},
		},
	}

	result := ConvertProvidersToModelList(cfg)

	if len(result) < 1 {
		t.Fatalf("len(result) = %d, want at least 1", len(result))
	}

	// First provider should use userModel as ModelName for backward compatibility
	if result[0].ModelName != "openrouter/auto" {
		t.Errorf("ModelName = %q, want %q", result[0].ModelName, "openrouter/auto")
	}

	// Model should NOT have duplicated prefix
	if result[0].Model != "openrouter/auto" {
		t.Errorf("Model = %q, want %q (should not duplicate prefix)", result[0].Model, "openrouter/auto")
	}
}
