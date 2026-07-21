// ClawEh - Personal AI Assistant
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package providers

import (
	"fmt"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/config"
	anthropicmessages "github.com/PivotLLM/spawnllm/anthropic_messages"
	"github.com/PivotLLM/spawnllm/azure"
	"github.com/PivotLLM/spawnllm/openai_compat"
	"github.com/PivotLLM/spawnllm/openai_responses"
)

// compatOpts builds the openai_compat options from the endpoint-scoped provider
// knobs and the model-scoped request knobs.
func compatOpts(model *config.ModelConfig, prov *config.Provider) []openai_compat.Option {
	return []openai_compat.Option{
		openai_compat.WithMaxTokensField(model.MaxTokensField),
		openai_compat.WithRequestTimeout(time.Duration(model.RequestTimeout) * time.Second),
		openai_compat.WithStrictCompat(prov.StrictCompat),
		openai_compat.WithNoParallelToolCalls(prov.NoParallelToolCalls),
		openai_compat.WithStrictAlternation(model.StrictAlternation),
		openai_compat.WithResponseLogFile(model.ResponseLogFile),
		openai_compat.WithReasoningEffort(model.ReasoningEffort),
		openai_compat.WithExtraBody(model.ExtraBody),
		openai_compat.WithDropParams(model.DropParams),
		openai_compat.WithModelLabel(model.ModelName),
		openai_compat.WithProtocol(prov.Protocol),
		openai_compat.WithResponseFormatJSONCapable(prov.ResponseFormatJSON),
	}
}

// responsesOpts builds the openai_responses options from the provider- and
// model-scoped knobs that apply to the Responses API (a subset of compatOpts —
// no max_tokens_field/strict_compat/strict_alternation, which are chat-only).
func responsesOpts(model *config.ModelConfig, prov *config.Provider) []openai_responses.Option {
	return []openai_responses.Option{
		openai_responses.WithRequestTimeout(time.Duration(model.RequestTimeout) * time.Second),
		openai_responses.WithNoParallelToolCalls(prov.NoParallelToolCalls),
		openai_responses.WithReasoningEffort(model.ReasoningEffort),
		openai_responses.WithExtraBody(model.ExtraBody),
		openai_responses.WithDropParams(model.DropParams),
		openai_responses.WithModelLabel(model.ModelName),
		openai_responses.WithProtocol(prov.Protocol),
		openai_responses.WithResponseFormatJSONCapable(prov.ResponseFormatJSON),
		openai_responses.WithResponseLogFile(model.ResponseLogFile),
	}
}

// CreateProviderFromConfig builds the LLM provider for a model reached through
// the given named provider. prov supplies the wire protocol, base URL,
// credentials, and endpoint-scoped knobs; model supplies the raw model id and
// request-scoped knobs. Returns the provider, the raw model id, and any error.
func CreateProviderFromConfig(model *config.ModelConfig, prov *config.Provider) (LLMProvider, string, error) {
	if model == nil {
		return nil, "", fmt.Errorf("model config is nil")
	}
	if model.Model == "" {
		return nil, "", fmt.Errorf("model is required")
	}
	if prov == nil {
		return nil, "", fmt.Errorf("provider is nil for model %q", model.ModelName)
	}

	modelID := model.Model

	switch prov.Protocol {
	case "openai-chat":
		return NewHTTPProviderWithOptions(prov.APIKey, prov.BaseURL, prov.Proxy, compatOpts(model, prov)...), modelID, nil

	case "openai-responses":
		return openai_responses.NewProvider(prov.APIKey, prov.BaseURL, prov.Proxy, responsesOpts(model, prov)...), modelID, nil

	case "azure":
		return azure.NewProviderWithTimeout(prov.APIKey, prov.BaseURL, prov.Proxy, model.RequestTimeout), modelID, nil

	case "anthropic":
		if prov.APIKey == "" {
			return nil, "", fmt.Errorf("provider %q: api_key required for anthropic protocol", prov.Name)
		}
		return NewHTTPProviderWithOptions(prov.APIKey, prov.BaseURL, prov.Proxy, compatOpts(model, prov)...), modelID, nil

	case "anthropic-messages":
		if prov.APIKey == "" {
			return nil, "", fmt.Errorf("provider %q: api_key required for anthropic-messages protocol", prov.Name)
		}
		return anthropicmessages.NewProviderWithTimeout(prov.APIKey, prov.BaseURL, model.RequestTimeout), modelID, nil

	case "claude-cli":
		return newCLIProvider(NewClaudeCliProvider, NewClaudeCliProviderWithTimeout, model, prov), modelID, nil

	case "codex-cli":
		return newCLIProvider(NewCodexCliProvider, NewCodexCliProviderWithTimeout, model, prov), modelID, nil

	case "gemini-cli":
		return newCLIProvider(NewGeminiCliProvider, NewGeminiCliProviderWithTimeout, model, prov), modelID, nil

	case "cursor-cli":
		return newCLIProvider(NewCursorCliProvider, NewCursorCliProviderWithTimeout, model, prov), modelID, nil

	default:
		return nil, "", fmt.Errorf("provider %q: unknown protocol %q", prov.Name, prov.Protocol)
	}
}

// newCLIProvider builds a subprocess CLI provider, applying the request timeout
// when set. The binary path comes from the provider (Command); workspace and
// CLI args/env come from the model.
func newCLIProvider[T LLMProvider](
	plain func(command, workspace string, extraArgs []string, env map[string]string) T,
	withTimeout func(command, workspace string, timeout time.Duration, extraArgs []string, env map[string]string) T,
	model *config.ModelConfig,
	prov *config.Provider,
) LLMProvider {
	workspace := model.Workspace
	if workspace == "" {
		workspace = "."
	}
	if model.RequestTimeout > 0 {
		return withTimeout(prov.Command, workspace, time.Duration(model.RequestTimeout)*time.Second, model.ExtraArgs, model.Env)
	}
	return plain(prov.Command, workspace, model.ExtraArgs, model.Env)
}
