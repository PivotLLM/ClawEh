// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

// The LLM provider clients, protocol DTOs, and the provider interfaces now live
// in the standalone github.com/PivotLLM/spawnllm module so ClawEh and Maestro
// can share the dispatch core (single binary) without import cycles. This file
// re-exports them under the historical providers names via aliases, so the
// policy/config code that stays here (dispatcher, factory, fallback, cooldown)
// and the rest of ClawEh compile unchanged. Add new code against either name —
// they are identical types.

package providers

import (
	"github.com/PivotLLM/ctxengine"
	"github.com/PivotLLM/spawnllm"
)

// MessageTypeToolError marks a tool result that reports a failure. Defined by
// the context engine, which reads it in its eviction sweep; re-exported here
// so the loop and the tools keep their historical name for it.
const MessageTypeToolError = ctxengine.MessageTypeToolError

// Interfaces + concrete provider types.
type (
	LLMProvider            = spawnllm.LLMProvider
	StatefulProvider       = spawnllm.StatefulProvider
	ThinkingCapable        = spawnllm.ThinkingCapable
	CLIProvider            = spawnllm.CLIProvider
	HTTPProvider           = spawnllm.HTTPProvider
	ClaudeProvider         = spawnllm.ClaudeProvider
	ClaudeCliProvider      = spawnllm.ClaudeCliProvider
	CodexCliProvider       = spawnllm.CodexCliProvider
	AntigravityCliProvider = spawnllm.AntigravityCliProvider
	CursorCliProvider      = spawnllm.CursorCliProvider
	ModelConfig            = spawnllm.ModelConfig
)

// Token streaming: a caller sets TextDeltaFunc under the TextDeltaOption key in
// the options map passed to provider.Chat to opt into per-delta assistant text
// streaming (honored by openai_compat/openai_responses; ignored elsewhere).
type TextDeltaFunc = spawnllm.TextDeltaFunc

// Protocol DTOs.
type (
	Message                = spawnllm.Message
	MessageAttachment      = spawnllm.MessageAttachment
	ToolCall               = spawnllm.ToolCall
	FunctionCall           = spawnllm.FunctionCall
	ToolDefinition         = spawnllm.ToolDefinition
	ToolFunctionDefinition = spawnllm.ToolFunctionDefinition
	LLMResponse            = spawnllm.LLMResponse
	UsageInfo              = spawnllm.UsageInfo
	DispatchStatus         = spawnllm.DispatchStatus
	ContentBlock           = spawnllm.ContentBlock
	CacheControl           = spawnllm.CacheControl
	ExtraContent           = spawnllm.ExtraContent
)

// Failover classification.
type (
	FailoverReason = spawnllm.FailoverReason
	FailoverError  = spawnllm.FailoverError
)

const (
	// TextDeltaOption is the options-map key for a providers.TextDeltaFunc; see
	// TextDeltaFunc above.
	TextDeltaOption = spawnllm.TextDeltaOption

	FailoverAuth         = spawnllm.FailoverAuth
	FailoverRateLimit    = spawnllm.FailoverRateLimit
	FailoverBilling      = spawnllm.FailoverBilling
	FailoverTimeout      = spawnllm.FailoverTimeout
	FailoverFormat       = spawnllm.FailoverFormat
	FailoverOverloaded   = spawnllm.FailoverOverloaded
	FailoverUnknown      = spawnllm.FailoverUnknown
	FailoverContextLimit = spawnllm.FailoverContextLimit
)

// Constructors + helpers.
var (
	NewHTTPProviderWithOptions           = spawnllm.NewHTTPProviderWithOptions
	NewUnconfiguredProvider              = spawnllm.NewUnconfiguredProvider
	NewClaudeCliProvider                 = spawnllm.NewClaudeCliProvider
	NewClaudeCliProviderWithTimeout      = spawnllm.NewClaudeCliProviderWithTimeout
	NewCodexCliProvider                  = spawnllm.NewCodexCliProvider
	NewCodexCliProviderWithTimeout       = spawnllm.NewCodexCliProviderWithTimeout
	NewAntigravityCliProvider            = spawnllm.NewAntigravityCliProvider
	NewAntigravityCliProviderWithTimeout = spawnllm.NewAntigravityCliProviderWithTimeout
	NewCursorCliProvider                 = spawnllm.NewCursorCliProvider
	NewCursorCliProviderWithTimeout      = spawnllm.NewCursorCliProviderWithTimeout

	ModelKey                = spawnllm.ModelKey
	splitModelKey           = spawnllm.SplitModelKey
	ClassifyError           = spawnllm.ClassifyError
	ReasonText              = spawnllm.ReasonText
	NormalizeToolCall       = spawnllm.NormalizeToolCall
	ParseModelRef           = spawnllm.ParseModelRef
	AgentIDFromContext      = spawnllm.AgentIDFromContext
	WithAgentID             = spawnllm.WithAgentID
	IsImageDimensionError   = spawnllm.IsImageDimensionError
	IsImageSizeError        = spawnllm.IsImageSizeError
	JSONObjectFortification = spawnllm.JSONObjectFortification
)
