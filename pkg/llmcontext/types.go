// ClawEh
// License: MIT

package llmcontext

import (
	"context"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/providers"
)

// LLMClient calls an LLM and returns a single response message.
type LLMClient interface {
	Complete(ctx context.Context, messages []providers.Message) (providers.Message, error)
}

// ModelChain records which LLM chain is configured for compression (for stats and logging).
type ModelChain struct {
	Primary   string   `json:"primary,omitempty"`
	Fallbacks []string `json:"fallbacks,omitempty"`
}

// ContextStats holds observable state for a session's context.
type ContextStats struct {
	TotalMessages       int
	MeaningfulMessages  int
	EstimatedTokens     int
	ContextWindowPct    float64
	LastCompressedAt    time.Time
	LastCompressionGain float64
	CompressionCooling  bool
	CoolingSinceCount   int
	// SummaryTokens is the estimated token count of the stored summary (runes/4).
	// Zero when no summary has been generated.
	SummaryTokens int
}

// MessageBuilder builds the full message slice sent to an LLM, including the
// system prompt, history, optional summary, and current message.
// pkg/agent.ContextBuilder satisfies this interface via structural typing.
type MessageBuilder interface {
	BuildMessages(
		history []providers.Message,
		summary, currentMessage string,
		media []string,
		channel, chatID string,
	) []providers.Message
}
