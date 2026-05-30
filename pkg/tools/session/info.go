// ClawEh
// License: MIT

package session

import (
	"context"
	"encoding/json"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/tools"
)

// SessionInfoFunc is the callback type supplied at tool construction time.
type SessionInfoFunc func(ctx context.Context, sessionKey string) (*tools.SessionInfo, error)

// SessionInfoTool implements the session_info MCP tool.
type SessionInfoTool struct {
	infoFn SessionInfoFunc
}

// NewSessionInfoTool creates a SessionInfoTool with the given info callback.
func NewSessionInfoTool(infoFn SessionInfoFunc) *SessionInfoTool {
	return &SessionInfoTool{infoFn: infoFn}
}

func (t *SessionInfoTool) Name() string          { return "session_info" }
func (t *SessionInfoTool) IsSessionScoped() bool { return true }

func (t *SessionInfoTool) Description() string {
	return "Return metadata about the current session: session key, start time, channel, " +
		"message count, archive sequence range, and the seq range covered by the current summary. " +
		"Use to orient yourself after a context compression or to understand how much history is available."
}

func (t *SessionInfoTool) Parameters() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
}

func (t *SessionInfoTool) Execute(ctx context.Context, _ map[string]any) *tools.ToolResult {
	sessionKey := tools.ToolSessionKey(ctx)
	if sessionKey == "" {
		return tools.ErrorResult("session key not available")
	}
	if t.infoFn == nil {
		return tools.ErrorResult("session info function not configured")
	}
	info, err := t.infoFn(ctx, sessionKey)
	if err != nil {
		return tools.ErrorResult("session info error: " + err.Error())
	}
	out, _ := json.Marshal(info)
	return &tools.ToolResult{ForLLM: string(out)}
}

// Ensure time package is used (for SessionInfo which references time.Time)
var _ = time.Time{}
