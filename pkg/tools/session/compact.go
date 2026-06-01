// ClawEh
// License: MIT

package session

import (
	"context"
	"errors"
	"fmt"

	"github.com/PivotLLM/ClawEh/pkg/llmcontext"
	"github.com/PivotLLM/ClawEh/pkg/tools"
)

// SessionCompactTool implements the session_compact MCP tool.
// It triggers an immediate context compaction for the current session.
// Session scoping uses the key injected via WithSessionKey (see base.go).
type SessionCompactTool struct {
	compact func(ctx context.Context, sessionKey string) (string, error)
}

// NewSessionCompactTool creates a SessionCompactTool with the given compact
// callback. The callback returns a human-readable compaction report and an error.
func NewSessionCompactTool(compact func(ctx context.Context, sessionKey string) (string, error)) *SessionCompactTool {
	return &SessionCompactTool{compact: compact}
}

func (t *SessionCompactTool) Name() string          { return "session_compact" }
func (t *SessionCompactTool) IsSessionScoped() bool { return true }

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

func (t *SessionCompactTool) Execute(ctx context.Context, _ map[string]any) *tools.ToolResult {
	sessionKey := tools.ToolSessionKey(ctx)
	if sessionKey == "" {
		return tools.ErrorResult("session key not available")
	}
	if t.compact == nil {
		return tools.ErrorResult("compact function not configured")
	}
	report, err := t.compact(ctx, sessionKey)
	// The report (when present) already describes the outcome — attempts and a
	// success/failure/nothing final line — so surface it verbatim to the LLM.
	if report != "" {
		return &tools.ToolResult{ForLLM: report}
	}
	if err != nil {
		if errors.Is(err, llmcontext.ErrNothingToCompress) {
			return &tools.ToolResult{ForLLM: "Session is already compact — nothing to summarize."}
		}
		return tools.ErrorResult(fmt.Sprintf("compaction failed: %v", err))
	}
	return &tools.ToolResult{ForLLM: "Session compacted successfully."}
}
