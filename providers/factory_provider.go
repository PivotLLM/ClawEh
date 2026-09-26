// ClawEh
// License: MIT

package providers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/PivotLLM/spawnllm"
	anthropicmessages "github.com/PivotLLM/spawnllm/anthropic_messages"
	"github.com/PivotLLM/spawnllm/azure"
	"github.com/PivotLLM/spawnllm/openai_compat"
	"github.com/PivotLLM/spawnllm/openai_responses"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/internal/childenv"
	"github.com/PivotLLM/ClawEh/logger"
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
		return nil, "", errors.New("model config is nil")
	}
	if model.Model == "" {
		return nil, "", errors.New("model is required")
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

	// "anthropic" and "anthropic-messages" are the same thing: Anthropic speaks
	// one wire format, the Messages API. "anthropic" previously routed to the
	// OpenAI-compatible provider, so a config naming it sent an OpenAI-shaped
	// request to api.anthropic.com and had SystemParts stripped on the way out —
	// wrong, and silently so, since the protocol name is in the WebUI's dropdown
	// alongside the one that works.
	case "anthropic", "anthropic-messages":
		if prov.APIKey == "" {
			return nil, "", fmt.Errorf("provider %q: api_key required for %s protocol", prov.Name, prov.Protocol)
		}
		return anthropicmessages.NewProviderWithTimeout(prov.APIKey, prov.BaseURL, model.RequestTimeout), modelID, nil

	case "claude-cli":
		return newCLIProvider(NewClaudeCliProvider, NewClaudeCliProviderWithTimeout, model, prov), modelID, nil

	case "codex-cli":
		return newCLIProvider(NewCodexCliProvider, NewCodexCliProviderWithTimeout, model, prov), modelID, nil

	// "gemini-cli" is an alias, not a second provider: Google deprecated the
	// Gemini CLI in favour of Antigravity, so a config still naming it keeps
	// starting and runs agy instead of failing on an unknown protocol.
	case "antigravity-cli", "gemini-cli":
		return newCLIProvider(NewAntigravityCliProvider, NewAntigravityCliProviderWithTimeout, model, prov), modelID, nil

	case "cursor-cli":
		return newCLIProvider(NewCursorCliProvider, NewCursorCliProviderWithTimeout, model, prov), modelID, nil

	default:
		return nil, "", fmt.Errorf("provider %q: unknown protocol %q", prov.Name, prov.Protocol)
	}
}

// newCLIProvider builds a subprocess CLI provider, applying the request timeout
// when set. The binary path comes from the provider (Command); the workspace
// comes from the model.
//
// Arguments and environment are the protocol's required ones plus whatever the
// model adds, with the permission-bypass flag included only when the provider's
// bypass_restrictions is on. They are supplied here rather than stored on every
// model so the catalogue, not each model entry, decides what a CLI runs with.
//
// With bypass off, a CLI that cannot approve a tool call refuses it and returns
// success with an empty answer, so the assistant would go silent instead of
// reporting a problem. The returned provider is wrapped to turn that into an
// error naming the setting.
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
	args := config.CLIArgs(prov.Protocol, prov.BypassRestrictions, model.ExtraArgs)
	env := config.CLIEnv(prov.Protocol, model.Env)
	var p LLMProvider
	if model.RequestTimeout > 0 {
		p = withTimeout(prov.Command, workspace, time.Duration(model.RequestTimeout)*time.Second, args, env)
	} else {
		p = plain(prov.Command, workspace, args, env)
	}
	// The CLI starts from the allowlisted environment, not the host's whole one,
	// so the service's own secrets never reach the subprocess; the per-model env
	// is overlaid on top by spawnllm.
	if s, ok := p.(spawnllm.BaseEnvSetter); ok {
		s.SetBaseEnv(childenv.CLI())
	}
	if prov.BypassRestrictions {
		return p
	}
	label := prov.Protocol
	if agent := config.CLIAgentByProtocol(prov.Protocol); agent != nil {
		label = agent.Label
	}
	return &cliDeclinedGuard{LLMProvider: p, label: label}
}

// cliDeclinedGuard wraps a CLI provider running without its permission-bypass
// flag. A CLI that refuses a tool call it cannot prompt for reports success
// with no text (agy also names the denied action), which would reach the user
// as silence. The guard turns that into an error that says what to change.
type cliDeclinedGuard struct {
	LLMProvider
	label string
}

// IsCLI marks the wrapped provider as a CLI, as the agent loop checks.
func (g *cliDeclinedGuard) IsCLI() bool { return true }

func (g *cliDeclinedGuard) Chat(ctx context.Context, messages []Message, tools []ToolDefinition, model string, options map[string]any) (*LLMResponse, error) {
	resp, err := g.LLMProvider.Chat(ctx, messages, tools, model, options)
	switch {
	case err != nil && strings.Contains(err.Error(), "denied"):
		return resp, &CLIDeclinedError{Message: g.declinedMessage(), Cause: err}
	case err == nil && resp != nil && strings.TrimSpace(resp.Content) == "" && len(resp.ToolCalls) == 0:
		return resp, &CLIDeclinedError{Message: g.declinedMessage()}
	}
	return resp, err
}

// CLIDeclinedError is the guard's error for a CLI that refused a tool call.
// Its text is written for the user, so the failover renderer shows it verbatim
// instead of the classification the fallback chain would otherwise report.
type CLIDeclinedError struct {
	Message string // what to change, naming the WebUI setting
	Cause   error  // the CLI's own error (e.g. the denied action), when it gave one
}

func (e *CLIDeclinedError) Error() string {
	if e.Cause != nil {
		return e.Message + ": " + e.Cause.Error()
	}
	return e.Message
}

func (e *CLIDeclinedError) Unwrap() error { return e.Cause }

func (g *cliDeclinedGuard) declinedMessage() string {
	return "The " + g.label + " declined to use tools. Tick *Bypass CLI restrictions* for this CLI in the WebUI, or allow the tools in the CLI's own settings."
}

// logBypassEnabled writes one INFO line per CLI provider running with its
// permission-bypass flag, so a startup log states what the CLI may do.
func logBypassEnabled(cfg *config.Config) {
	if cfg == nil {
		return
	}
	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		if p.BypassRestrictions && config.IsCLIProtocol(p.Protocol) {
			logger.InfoCF("provider", "Bypass CLI restrictions is on: the CLI can run commands and edit files anywhere the service user can, without asking",
				map[string]any{"provider": p.Name, "protocol": p.Protocol})
		}
	}
}
