// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

// The LLM provider clients, protocol DTOs, and the provider interfaces now live
// in the standalone github.com/PivotLLM/spawnllm module so ClawEh and Maestro
// can share the dispatch core (single binary) without import cycles. This file
// re-exports them under the historical pkg/providers names via aliases, so the
// policy/config code that stays here (dispatcher, factory, fallback, cooldown)
// and the rest of ClawEh compile unchanged. Add new code against either name —
// they are identical types.

package providers

import "github.com/PivotLLM/spawnllm"

// Interfaces + concrete provider types.
type (
	LLMProvider       = spawnllm.LLMProvider
	StatefulProvider  = spawnllm.StatefulProvider
	ThinkingCapable   = spawnllm.ThinkingCapable
	CLIProvider       = spawnllm.CLIProvider
	HTTPProvider      = spawnllm.HTTPProvider
	ClaudeProvider    = spawnllm.ClaudeProvider
	ClaudeCliProvider = spawnllm.ClaudeCliProvider
	CodexCliProvider  = spawnllm.CodexCliProvider
	GeminiCliProvider = spawnllm.GeminiCliProvider
	ModelConfig       = spawnllm.ModelConfig
)

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
	NewHTTPProviderWithOptions       = spawnllm.NewHTTPProviderWithOptions
	NewUnconfiguredProvider          = spawnllm.NewUnconfiguredProvider
	NewClaudeProviderWithTokenSource = spawnllm.NewClaudeProviderWithTokenSource
	NewClaudeCliProvider             = spawnllm.NewClaudeCliProvider
	NewClaudeCliProviderWithTimeout  = spawnllm.NewClaudeCliProviderWithTimeout
	NewCodexCliProvider              = spawnllm.NewCodexCliProvider
	NewCodexCliProviderWithTimeout   = spawnllm.NewCodexCliProviderWithTimeout
	NewGeminiCliProvider             = spawnllm.NewGeminiCliProvider
	NewGeminiCliProviderWithTimeout  = spawnllm.NewGeminiCliProviderWithTimeout

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
