// ClawEh
// License: MIT

package tools

import (
	"context"
	"fmt"
)

// SessionCompactTool implements the compact_session MCP tool.
// It triggers an immediate context compaction for the current session.
// Session scoping uses the key injected via WithSessionKey (see base.go).
type SessionCompactTool struct {
	compact func(ctx context.Context, sessionKey string) error
}

// NewSessionCompactTool creates a SessionCompactTool with the given compact callback.
// The callback is called with the resolved session key when the tool executes.
func NewSessionCompactTool(compact func(ctx context.Context, sessionKey string) error) *SessionCompactTool {
	return &SessionCompactTool{compact: compact}
}

func (t *SessionCompactTool) Name() string { return "compact_session" }

func (t *SessionCompactTool) Description() string {
	return "Trigger an immediate context compaction for the current session. " +
		"Use after completing a major task to summarise prior messages and free context window space. " +
		"The summary is preserved and injected into the next response."
}

func (t *SessionCompactTool) Parameters() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
}

func (t *SessionCompactTool) Execute(ctx context.Context, _ map[string]any) *ToolResult {
	sessionKey := ToolSessionKey(ctx)
	if sessionKey == "" {
		return ErrorResult("session key not available")
	}
	if t.compact == nil {
		return ErrorResult("compact function not configured")
	}
	if err := t.compact(ctx, sessionKey); err != nil {
		return ErrorResult(fmt.Sprintf("compaction failed: %v", err))
	}
	return &ToolResult{ForLLM: "Session compacted successfully."}
}
