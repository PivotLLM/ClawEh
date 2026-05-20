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
	// AddToolCallMessage records the assistant turn containing tool calls.
	// Writes to session store and archive. Increments msgCount.
	// Does NOT trigger compression (deferred to PreDispatchCheck).
	AddToolCallMessage(ctx context.Context, msg providers.Message) error
	// AddToolResult records a tool result message.
	// Writes to session store and archive. Increments msgCount.
	// Does NOT trigger compression (deferred to PreDispatchCheck).
	AddToolResult(ctx context.Context, msg providers.Message) error
	// PreDispatchCheck runs the compression trigger check and, if compression
	// fires and succeeds, rebuilds the message slice via Build() and returns it.
	// If no compression is needed, returns current unchanged.
	// Returns ErrCompressionFailed if compression was attempted but failed;
	// callers may proceed with the stale slice in that case (best-effort).
	PreDispatchCheck(ctx context.Context, current []providers.Message) ([]providers.Message, error)
	// CheckAndCompress estimates the post-Build token count (including the
	// configured overhead for system prompt, tool definitions, and completion budget)
	// and compresses if the adjusted total exceeds the normal or safety threshold.
	// ToolCalls arguments are included in the token estimate (not just Content).
	// If compression fires and succeeds, rebuilds via Build() and returns the fresh
	// slice. Returns the input unchanged if no compression is needed.
	// The cooldown mechanism prevents double-firing when PreDispatchCheck already
	// ran on the same turn.
	CheckAndCompress(ctx context.Context, built []providers.Message) ([]providers.Message, error)
	// SetSystemPrompt sets the static system prompt injected at Build time.
	SetSystemPrompt(prompt string)
	// SetCallContext records the channel and chatID for the current call so that
	// Build() can pass them to the MessageBuilder's system-prompt construction.
	SetCallContext(channel, chatID string)
	// SetSessionToken sets the per-session MCP session token injected into the
	// system prompt by Build(). The LLM must supply this token as the
	// session_token parameter on session-scoped mcp__claw__* tool calls.
	// An empty token disables injection.
	SetSessionToken(token string)
	// Build returns the full message slice ready to send to the LLM.
	Build(ctx context.Context) ([]providers.Message, error)
	// Compact triggers a normal LLM-based compression pass on demand, identical
	// to the path taken when the regular compression threshold is crossed.
	Compact(ctx context.Context) error
	// ForceCompress aggressively reduces context when the hard limit is hit.
	ForceCompress(ctx context.Context) error
	// Stats returns the current observable state of this context.
	Stats() ContextStats
	// Reset clears all history, summary, in-memory compression state, and
	// deletes the archive for this session. The session can continue normally
	// after Reset; archive and state are recreated on demand.
	Reset(ctx context.Context) error
	// Close flushes durable compaction state and closes the archive connection.
	// After Close the manager must not be used. It is called by the AgentLoop
	// eviction goroutine and during shutdown to release all held resources.
	Close(ctx context.Context) error
}
