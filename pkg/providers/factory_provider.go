// PicoClaw - Ultra-lightweight personal AI agent
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package providers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/config"
	anthropicmessages "github.com/PivotLLM/ClawEh/pkg/providers/anthropic_messages"
	"github.com/PivotLLM/ClawEh/pkg/providers/azure"
	"github.com/PivotLLM/ClawEh/pkg/providers/bedrock"
	"github.com/PivotLLM/ClawEh/pkg/providers/openai_compat"
)

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

// ExtractProtocol extracts the protocol prefix and model identifier from a model string.
// If no prefix is specified, it defaults to "openai".
// Examples:
//   - "openai/gpt-4o" -> ("openai", "gpt-4o")
//   - "anthropic/claude-sonnet-4.6" -> ("anthropic", "claude-sonnet-4.6")
//   - "gpt-4o" -> ("openai", "gpt-4o")  // default protocol
func ExtractProtocol(model string) (protocol, modelID string) {
	model = strings.TrimSpace(model)
	protocol, modelID, found := strings.Cut(model, "/")
	if !found {
		return "openai", model
	}
	return protocol, modelID
}

// CreateProviderFromConfig creates a provider based on the ModelConfig.
// It uses the protocol prefix in the Model field to determine which provider to create.
// Supported protocols: openai, litellm, anthropic, anthropic-messages,
// claude-cli, codex-cli
// Returns the provider, the model ID (without protocol prefix), and any error.
func CreateProviderFromConfig(cfg *config.ModelConfig) (LLMProvider, string, error) {
	if cfg == nil {
		return nil, "", fmt.Errorf("config is nil")
	}

	if cfg.Model == "" {
		return nil, "", fmt.Errorf("model is required")
	}

	protocol, modelID := ExtractProtocol(cfg.Model)

	switch protocol {
	case "openai":
		// OpenAI with API key
		if cfg.APIKey == "" && cfg.APIBase == "" {
			return nil, "", fmt.Errorf("api_key or api_base is required for HTTP-based protocol %q", protocol)
		}
		apiBase := cfg.APIBase
		if apiBase == "" {
			apiBase = getDefaultAPIBase(protocol)
		}
		opts := []openai_compat.Option{
			openai_compat.WithMaxTokensField(cfg.MaxTokensField),
			openai_compat.WithRequestTimeout(time.Duration(cfg.RequestTimeout) * time.Second),
			openai_compat.WithStrictCompat(cfg.StrictCompat),
		}
		return NewHTTPProviderWithOptions(cfg.APIKey, apiBase, cfg.Proxy, opts...), modelID, nil

	case "azure", "azure-openai":
		// Azure OpenAI uses deployment-based URLs, api-key header auth,
		// and always sends max_completion_tokens.
		if cfg.APIKey == "" {
			return nil, "", fmt.Errorf("api_key is required for azure protocol")
		}
		if cfg.APIBase == "" {
			return nil, "", fmt.Errorf(
				"api_base is required for azure protocol (e.g., https://your-resource.openai.azure.com)",
			)
		}
		return azure.NewProviderWithTimeout(
			cfg.APIKey,
			cfg.APIBase,
			cfg.Proxy,
			cfg.RequestTimeout,
		), modelID, nil

	case "bedrock":
		// AWS Bedrock uses AWS SDK credentials (env vars, profiles, IAM roles, etc.)
		// api_base can be:
		//   - A region name: us-east-1 (AWS SDK resolves endpoint automatically)
		//   - A full endpoint URL: https://bedrock-runtime.us-east-1.amazonaws.com
		// api_key (optional): one of:
		//   - An Amazon Bedrock API key (bearer token) — no colon, e.g. "bak-abc123..."
		//   - "ACCESS_KEY_ID:SECRET_ACCESS_KEY" or "ACCESS_KEY_ID:SECRET_ACCESS_KEY:SESSION_TOKEN"
		//   If omitted, the AWS default credential chain is used (env vars, ~/.aws, IAM roles).
		var opts []bedrock.Option
		if cfg.APIKey != "" {
			if strings.Contains(cfg.APIKey, ":") {
				parts := strings.SplitN(cfg.APIKey, ":", 3)
				sessionToken := ""
				if len(parts) == 3 {
					sessionToken = parts[2]
				}
				opts = append(opts, bedrock.WithStaticCredentials(parts[0], parts[1], sessionToken))
			} else {
				opts = append(opts, bedrock.WithBearerToken(cfg.APIKey))
			}
		}
		if cfg.APIBase != "" {
			if !strings.Contains(cfg.APIBase, "://") {
				// Treat as region
				opts = append(opts, bedrock.WithRegion(cfg.APIBase))
			} else {
				// Full endpoint URL (for custom endpoints or testing)
				opts = append(opts, bedrock.WithBaseEndpoint(cfg.APIBase))
			}
		}
		// Use a separate timeout for AWS config loading (credential resolution can block)
		initTimeout := 30 * time.Second
		if cfg.RequestTimeout > 0 {
			reqTimeout := time.Duration(cfg.RequestTimeout) * time.Second
			opts = append(opts, bedrock.WithRequestTimeout(reqTimeout))
			if reqTimeout > initTimeout {
				initTimeout = reqTimeout
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), initTimeout)
		defer cancel()
		provider, err := bedrock.NewProvider(ctx, opts...)
		if err != nil {
			return nil, "", fmt.Errorf("creating bedrock provider: %w", err)
		}
		return provider, modelID, nil

	case "litellm", "openrouter", "groq", "gemini", "nvidia",
		"ollama", "moonshot", "deepseek", "cerebras",
		"vllm", "qwen", "mistral", "avian":
		// All other OpenAI-compatible HTTP providers
		if cfg.APIKey == "" && cfg.APIBase == "" {
			return nil, "", fmt.Errorf("api_key or api_base is required for HTTP-based protocol %q", protocol)
		}
		apiBase := cfg.APIBase
		if apiBase == "" {
			apiBase = getDefaultAPIBase(protocol)
		}
		opts := []openai_compat.Option{
			openai_compat.WithMaxTokensField(cfg.MaxTokensField),
			openai_compat.WithRequestTimeout(time.Duration(cfg.RequestTimeout) * time.Second),
			openai_compat.WithStrictCompat(cfg.StrictCompat),
			// Groq's Llama models fail tool calls when parallel_tool_calls is enabled.
			openai_compat.WithNoParallelToolCalls(protocol == "groq"),
		}
		return NewHTTPProviderWithOptions(cfg.APIKey, apiBase, cfg.Proxy, opts...), modelID, nil

	case "anthropic":
		if cfg.AuthMethod == "oauth" || cfg.AuthMethod == "token" {
			// Use OAuth credentials from auth store
			provider, err := createClaudeAuthProvider()
			if err != nil {
				return nil, "", err
			}
			return provider, modelID, nil
		}
		// Use API key with HTTP API
		apiBase := cfg.APIBase
		if apiBase == "" {
			apiBase = "https://api.anthropic.com/v1"
		}
		if cfg.APIKey == "" {
			return nil, "", fmt.Errorf("api_key is required for anthropic protocol (model: %s)", cfg.Model)
		}
		opts := []openai_compat.Option{
			openai_compat.WithMaxTokensField(cfg.MaxTokensField),
			openai_compat.WithRequestTimeout(time.Duration(cfg.RequestTimeout) * time.Second),
			openai_compat.WithStrictCompat(cfg.StrictCompat),
		}
		return NewHTTPProviderWithOptions(cfg.APIKey, apiBase, cfg.Proxy, opts...), modelID, nil

	case "anthropic-messages":
		// Anthropic Messages API with native format (HTTP-based, no SDK)
		apiBase := cfg.APIBase
		if apiBase == "" {
			apiBase = "https://api.anthropic.com/v1"
		}
		if cfg.APIKey == "" {
			return nil, "", fmt.Errorf("api_key is required for anthropic-messages protocol (model: %s)", cfg.Model)
		}
		return anthropicmessages.NewProviderWithTimeout(
			cfg.APIKey,
			apiBase,
			cfg.RequestTimeout,
		), modelID, nil

	case "claude-cli", "claudecli":
		workspace := cfg.Workspace
		if workspace == "" {
			workspace = "."
		}
		if cfg.RequestTimeout > 0 {
			return NewClaudeCliProviderWithTimeout(cfg.Command, workspace, time.Duration(cfg.RequestTimeout)*time.Second, cfg.ExtraArgs, cfg.Env), modelID, nil
		}
		return NewClaudeCliProvider(cfg.Command, workspace, cfg.ExtraArgs, cfg.Env), modelID, nil

	case "codex-cli", "codexcli":
		workspace := cfg.Workspace
		if workspace == "" {
			workspace = "."
		}
		if cfg.RequestTimeout > 0 {
			return NewCodexCliProviderWithTimeout(cfg.Command, workspace, time.Duration(cfg.RequestTimeout)*time.Second, cfg.ExtraArgs, cfg.Env), modelID, nil
		}
		return NewCodexCliProvider(cfg.Command, workspace, cfg.ExtraArgs, cfg.Env), modelID, nil

	case "gemini-cli", "geminicli":
		workspace := cfg.Workspace
		if workspace == "" {
			workspace = "."
		}
		if cfg.RequestTimeout > 0 {
			return NewGeminiCliProviderWithTimeout(cfg.Command, workspace, time.Duration(cfg.RequestTimeout)*time.Second, cfg.ExtraArgs, cfg.Env), modelID, nil
		}
		return NewGeminiCliProvider(cfg.Command, workspace, cfg.ExtraArgs, cfg.Env), modelID, nil

	default:
		return nil, "", fmt.Errorf("unknown protocol %q in model %q", protocol, cfg.Model)
	}
}

// getDefaultAPIBase returns the default API base URL for a given protocol.
func getDefaultAPIBase(protocol string) string {
	switch protocol {
	case "openai":
		return "https://api.openai.com/v1"
	case "openrouter":
		return "https://openrouter.ai/api/v1"
	case "litellm":
		return "http://localhost:4000/v1"
	case "groq":
		return "https://api.groq.com/openai/v1"
	case "gemini":
		return "https://generativelanguage.googleapis.com/v1beta"
	case "nvidia":
		return "https://integrate.api.nvidia.com/v1"
	case "ollama":
		return "http://localhost:11434/v1"
	case "moonshot":
		return "https://api.moonshot.cn/v1"
	case "deepseek":
		return "https://api.deepseek.com/v1"
	case "cerebras":
		return "https://api.cerebras.ai/v1"
	case "qwen":
		return "https://dashscope.aliyuncs.com/compatible-mode/v1"
	case "vllm":
		return "http://localhost:8000/v1"
	case "mistral":
		return "https://api.mistral.ai/v1"
	case "avian":
		return "https://api.avian.io/v1"
	default:
		return ""
	}
}
