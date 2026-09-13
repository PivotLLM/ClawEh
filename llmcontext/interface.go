// ClawEh
// License: MIT

package llmcontext

import (
	"context"

	"github.com/PivotLLM/ClawEh/providers"
)

// Placement says where an injected block lands in the assembled request.
type Placement int

const (
	// PlaceSystemStable appends the block to the system message. Use it for
	// content that is the same on every turn of a session: it joins the cached
	// prompt prefix and is paid for once.
	PlaceSystemStable Placement = iota
	// PlaceCurrentUser folds the block into the latest user message. Use it for
	// content selected per turn: anything that varies must not sit ahead of the
	// history, or it invalidates the cached prefix for all of it.
	PlaceCurrentUser
)

// Injection is a block of prompt text a memory system (or any other caller)
// asks the manager to place in the assembled request. The manager places it,
// counts it against the budget, and never persists it: an injection lives only
// in the built slice.
type Injection struct {
	Placement Placement
	Text      string
}

// AssembleRequest carries what the manager cannot see for itself when it
// builds the request for one model call.
type AssembleRequest struct {
	// ToolDefinitionTokens is the estimated cost of the tool schemas the caller
	// will send with the request. Counted by every compaction trigger so they
	// measure the real request rather than stored history alone.
	ToolDefinitionTokens int
	// Injections are placed into the built slice in order.
	Injections []Injection
}

// Assembly is the result of one Assemble call.
type Assembly struct {
	// Messages is the full slice ready to send to the model, system message first.
	Messages []providers.Message
	// Evictions lists the LLM-free evictions the sweep performed before the
	// build (also DEBUG-logged), so the caller can surface a one-line notice.
	Evictions []EvictionEvent
	// Compacted reports that a safety-net compaction ran during this call.
	Compacted bool
}

// Changed reports whether stored history was rewritten by this call, by
// eviction or compaction, so a caller holding an earlier slice must take the
// fresh one.
func (a Assembly) Changed() bool { return len(a.Evictions) > 0 || a.Compacted }

// ContextManager owns the lifecycle of a session's conversational context:
// storage, the archive, context assembly, compaction, and statistics.
//
// The turn shape is: Add* the inbound message, Assemble before every model
// call, Add* what the model and the tools produced, repeat. Each Add returns
// the transcript seq the message was stored under, which is the number the
// rest of the system (summaries, session tools, memory evidence) cites.
type ContextManager interface {
	// AddUserMessage appends a user message and runs the turn-boundary
	// compaction check.
	AddUserMessage(ctx context.Context, msg providers.Message) (int64, error)
	// AddAssistantMessage appends an assistant message and runs the
	// turn-boundary compaction check.
	AddAssistantMessage(ctx context.Context, msg providers.Message) (int64, error)
	// AddToolCallMessage records the assistant turn containing tool calls.
	// No compaction check: that is deferred to the next Assemble.
	AddToolCallMessage(ctx context.Context, msg providers.Message) (int64, error)
	// AddToolResult records a tool result message. No compaction check.
	AddToolResult(ctx context.Context, msg providers.Message) (int64, error)

	// Assemble builds the request for one model call. It runs the LLM-free
	// eviction sweep, the emergency compaction checks (before the build on
	// stored history, after it on the built request), and places the
	// injections. It is safe to call once per iteration of a tool-using turn.
	Assemble(ctx context.Context, req AssembleRequest) (Assembly, error)

	// SetCallContext records the channel and chatID for the current call so
	// the system prompt receives the correct session context.
	SetCallContext(channel, chatID string)
	// SetSessionToken sets the per-session MCP session token injected into the
	// system prompt. An empty token disables injection.
	SetSessionToken(token string)

	// Compact triggers a normal LLM-based compression pass on demand.
	Compact(ctx context.Context) error
	// LastCompactionReport returns the report from the most recent pass, or nil.
	LastCompactionReport() *CompactionReport
	// RenderedSummary returns the current session summary as Markdown, or "".
	RenderedSummary() string
	// ForceCompress aggressively reduces context when the hard limit is hit.
	ForceCompress(ctx context.Context) error
	// Stats returns the current observable state of this context.
	Stats() ContextStats
	// Reset clears the live history, summary and in-memory compression state.
	// The archive is preserved. The session can continue normally after Reset.
	Reset(ctx context.Context) error
	// Close flushes durable compaction state and closes the archive. After
	// Close the manager must not be used.
	Close(ctx context.Context) error
}
