// ClawEh - Personal AI Assistant
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package providers

import (
	"fmt"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/auth"
	"github.com/PivotLLM/ClawEh/pkg/config"
	anthropicmessages "github.com/PivotLLM/ClawEh/pkg/providers/anthropic_messages"
	"github.com/PivotLLM/ClawEh/pkg/providers/azure"
	"github.com/PivotLLM/ClawEh/pkg/providers/openai_compat"
)

// getCredential is the auth-store lookup used by the Claude OAuth provider.
// Indirected through a package var so tests can stub it.
var getCredential = auth.GetCredential

// createClaudeAuthProvider creates a Claude provider using OAuth credentials from auth store.
func createClaudeAuthProvider() (LLMProvider, error) {
	cred, err := getCredential("anthropic")
	if err != nil {
		return nil, fmt.Errorf("loading auth credentials: %w", err)
	}
	if cred == nil {
		return nil, fmt.Errorf("no credentials for anthropic. Run: claw auth login --provider anthropic")
	}
	return NewClaudeProviderWithTokenSource(cred.AccessToken, createClaudeTokenSource()), nil
}

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
	case "openai":
		return NewHTTPProviderWithOptions(prov.APIKey, prov.BaseURL, prov.Proxy, compatOpts(model, prov)...), modelID, nil

	case "azure":
		return azure.NewProviderWithTimeout(prov.APIKey, prov.BaseURL, prov.Proxy, model.RequestTimeout), modelID, nil

	case "anthropic":
		// OAuth/token credentials use the native Claude auth provider; an api
		// key uses the OpenAI-compatible HTTP path against the Anthropic base.
		if prov.AuthMethod == "oauth" || prov.AuthMethod == "token" {
			p, err := createClaudeAuthProvider()
			if err != nil {
				return nil, "", err
			}
			return p, modelID, nil
		}
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
