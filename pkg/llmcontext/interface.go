// ClawEh
// License: MIT

package llmcontext

import (
	"context"

	"github.com/PivotLLM/ClawEh/pkg/providers"
)

// ContextManager owns the full lifecycle of a session's conversational context:
// storage coordination, context building, compression triggers, and statistics.
type ContextManager interface {
	// AddUserMessage appends a user message and triggers compression if needed.
	AddUserMessage(ctx context.Context, msg providers.Message) error
	// AddAssistantMessage appends an assistant message and triggers compression if needed.
	AddAssistantMessage(ctx context.Context, msg providers.Message) error
	// SetSystemPrompt sets the static system prompt injected at Build time.
	SetSystemPrompt(prompt string)
	// SetCallContext records the channel and chatID for the current call so that
	// Build() can pass them to the MessageBuilder's system-prompt construction.
	SetCallContext(channel, chatID string)
	// Build returns the full message slice ready to send to the LLM.
	Build(ctx context.Context) ([]providers.Message, error)
	// Compact triggers a normal LLM-based compression pass on demand, identical
	// to the path taken when the regular compression threshold is crossed.
	Compact(ctx context.Context) error
	// ForceCompress aggressively reduces context when the hard limit is hit.
	ForceCompress(ctx context.Context) error
	// Stats returns the current observable state of this context.
	Stats() ContextStats
	// Reset clears all history, summary, and compression state.
	Reset() error
}
