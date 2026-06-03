// ClawEh
// License: MIT

package session

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/PivotLLM/ClawEh/pkg/llmcontext"
	"github.com/PivotLLM/ClawEh/pkg/tools"
)

// SessionCompactTool implements the session_compact MCP tool.
// It triggers an immediate context compaction for the current session.
// Session scoping uses the key injected via WithSessionKey (see base.go).
type SessionCompactTool struct {
	compact func(ctx context.Context, sessionKey string) (report, summary string, err error)
}

// NewSessionCompactTool creates a SessionCompactTool with the given compact
// callback. The callback returns the compaction report, the resulting rendered
// summary, and an error.
func NewSessionCompactTool(compact func(ctx context.Context, sessionKey string) (report, summary string, err error)) *SessionCompactTool {
	return &SessionCompactTool{compact: compact}
}

func (t *SessionCompactTool) Name() string          { return "session_compact" }
func (t *SessionCompactTool) IsSessionScoped() bool { return true }

func (t *SessionCompactTool) Description() string {
	return "Trigger an immediate context compaction for the current session. " +
		"Use after completing a major task to summarise prior messages and free context window space. " +
		"The result returns the compaction report and the new summary now in your context; " +
		"pass an optional `message` to append a note to yourself (e.g. what to do next)."
}

func (t *SessionCompactTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"message": map[string]any{
				"type":        "string",
				"description": "Optional note appended to the result — e.g. a reminder of what to do next after compaction.",
			},
		},
	}
}

func (t *SessionCompactTool) Execute(ctx context.Context, args map[string]any) *tools.ToolResult {
	sessionKey := tools.ToolSessionKey(ctx)
	if sessionKey == "" {
		return tools.ErrorResult("session key not available")
	}
	if t.compact == nil {
		return tools.ErrorResult("compact function not configured")
	}
	message, _ := args["message"].(string)

	report, summary, err := t.compact(ctx, sessionKey)

	var b strings.Builder
	switch {
	case report != "":
		// The report already describes the outcome (attempts + final line).
		b.WriteString(report)
	case errors.Is(err, llmcontext.ErrNothingToCompress):
		b.WriteString("Session is already compact — nothing to summarize.")
	case err != nil:
		return tools.ErrorResult(fmt.Sprintf("compaction failed: %v", err))
	default:
		b.WriteString("Session compacted successfully.")
	}

	if s := strings.TrimSpace(summary); s != "" {
		b.WriteString("\n\n--- Current summary (now in your context) ---\n")
		b.WriteString(s)
	}
	if m := strings.TrimSpace(message); m != "" {
		b.WriteString("\n\n--- Your note ---\n")
		b.WriteString(m)
	}
	return &tools.ToolResult{ForLLM: b.String()}
}
