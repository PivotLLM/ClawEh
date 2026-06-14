package tools

import (
	"context"
	"sync/atomic"
)

// Tool is the interface that all tools must implement.
type Tool interface {
	Name() string
	Description() string
	Parameters() map[string]any
	Execute(ctx context.Context, args map[string]any) *ToolResult
}

// ToolAllowChecker is implemented by any type that can determine whether a
// named tool is permitted. config.AgentConfig satisfies this interface.
// Keeping it here avoids importing config into tools.
type ToolAllowChecker interface {
	IsToolAllowed(name string) bool
}

// --- Request-scoped tool context (channel / chatID / allow checker) ---
//
// Carried via context.Value so that concurrent tool calls each receive
// their own immutable copy — no mutable state on singleton tool instances.
//
// Keys are unexported pointer-typed vars — guaranteed collision-free,
// and only accessible through the helper functions below.

type toolCtxKey struct{ name string }

var (
	ctxKeyChannel      = &toolCtxKey{"channel"}
	ctxKeyChatID       = &toolCtxKey{"chatID"}
	ctxKeyAllowChecker = &toolCtxKey{"allowChecker"}
	ctxKeyRoundSent    = &toolCtxKey{"roundSent"}
	ctxKeySessionKey   = &toolCtxKey{"sessionKey"}
)

// WithToolContext returns a child context carrying channel and chatID.
func WithToolContext(ctx context.Context, channel, chatID string) context.Context {
	ctx = context.WithValue(ctx, ctxKeyChannel, channel)
	ctx = context.WithValue(ctx, ctxKeyChatID, chatID)
	return ctx
}

// ToolChannel extracts the channel from ctx, or "" if unset.
func ToolChannel(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyChannel).(string)
	return v
}

// ToolChatID extracts the chatID from ctx, or "" if unset.
func ToolChatID(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyChatID).(string)
	return v
}

// WithToolAllowChecker returns a child context carrying an allow checker.
// This is used by ExecuteWithContext for defense-in-depth enforcement.
func WithToolAllowChecker(ctx context.Context, checker ToolAllowChecker) context.Context {
	return context.WithValue(ctx, ctxKeyAllowChecker, checker)
}

// ToolAllowCheckerFromCtx extracts the ToolAllowChecker from ctx, or nil if unset.
func ToolAllowCheckerFromCtx(ctx context.Context) ToolAllowChecker {
	v, _ := ctx.Value(ctxKeyAllowChecker).(ToolAllowChecker)
	return v
}

// WithRoundSentFlag returns a child context carrying a per-round sent flag.
// Used by the concurrent agent loop to track whether the message tool fired.
func WithRoundSentFlag(ctx context.Context, flag *atomic.Bool) context.Context {
	return context.WithValue(ctx, ctxKeyRoundSent, flag)
}

// roundSentFlagFromCtx extracts the per-round sent flag, or nil if not set.
func roundSentFlagFromCtx(ctx context.Context) *atomic.Bool {
	v, _ := ctx.Value(ctxKeyRoundSent).(*atomic.Bool)
	return v
}

// RoundSentFlagFromCtx is the exported form of roundSentFlagFromCtx,
// available to sub-packages (e.g. pkg/tools/msg).
func RoundSentFlagFromCtx(ctx context.Context) *atomic.Bool {
	return roundSentFlagFromCtx(ctx)
}

// WithSessionKey returns a child context carrying the active session key.
// In the MCP dispatch path, mcpserver.dispatchToolCall injects the session key
// after validating the session_token parameter (see pkg/mcpserver/tools.go).
// In the direct agent loop path, the context carries the session key via
// the tool execution plumbing.
//
// Tools that call ToolSessionKey must implement the SessionScoped interface
// so the MCP dispatcher knows to inject the key. See SessionScoped below.
func WithSessionKey(ctx context.Context, key string) context.Context {
	return context.WithValue(ctx, ctxKeySessionKey, key)
}

// ToolSessionKey extracts the session key from ctx, or "" if unset.
func ToolSessionKey(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeySessionKey).(string)
	return v
}

// AsyncCallback is a function type that async tools use to notify completion.
// When an async tool finishes its work, it calls this callback with the result.
//
// The ctx parameter allows the callback to be canceled if the agent is shutting down.
// The result parameter contains the tool's execution result.
type AsyncCallback func(ctx context.Context, result *ToolResult)

// AsyncExecutor is an optional interface that tools can implement to support
// asynchronous execution with completion callbacks.
//
// Unlike the old AsyncTool pattern (SetCallback + Execute), AsyncExecutor
// receives the callback as a parameter of ExecuteAsync. This eliminates the
// data race where concurrent calls could overwrite each other's callbacks
// on a shared tool instance.
//
// This is useful for:
//   - Long-running operations that shouldn't block the agent loop
//   - Subagent spawns that complete independently
//   - Background tasks that need to report results later
//
// Example:
//
//	func (t *exampleTool) ExecuteAsync(ctx context.Context, args map[string]any, cb AsyncCallback) *ToolResult {
//	    go func() {
//	        result := t.runWork(ctx, args)
//	        if cb != nil { cb(ctx, result) }
//	    }()
//	    return AsyncResult("Work started, will report back")
//	}
type AsyncExecutor interface {
	Tool
	// ExecuteAsync runs the tool asynchronously. The callback cb will be
	// invoked (possibly from another goroutine) when the async operation
	// completes. cb is guaranteed to be non-nil by the caller (registry).
	ExecuteAsync(ctx context.Context, args map[string]any, cb AsyncCallback) *ToolResult
}

// SessionScoped is an optional interface for tools that require a session key
// to be injected into their execution context. When a tool implementing this
// interface is called via the MCP HTTP server, the dispatcher injects the
// resolved session key via WithSessionKey before Execute is called.
//
// Any tool that calls ToolSessionKey(ctx) must implement this interface.
// The MCP server checks for this interface at dispatch time instead of using
// a hardcoded list, so new session-scoped tools are handled automatically.
type SessionScoped interface {
	IsSessionScoped() bool
}

func ToolToSchema(tool Tool) map[string]any {
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        tool.Name(),
			"description": tool.Description(),
			"parameters":  tool.Parameters(),
		},
	}
}
