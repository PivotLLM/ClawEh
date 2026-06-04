// ClawEh
// License: MIT

package session

import (
	"context"

	"github.com/PivotLLM/ClawEh/pkg/tools"
)

// SessionClearTool implements the session_clear MCP tool. It clears the agent's
// active conversation (preserving the durable archive and summary log) and then
// hands the agent a fresh turn. An optional message is delivered to the agent as
// a self-authored handoff note on that fresh turn.
//
// The clear itself is deferred to a clean turn boundary by the agent loop (the
// tool publishes a reset-tagged inbound message); it never wipes history mid-turn.
// Off by default — enable only for agents intended to run autonomous task loops.
type SessionClearTool struct {
	clear func(ctx context.Context, sessionKey, message string) error
}

// NewSessionClearTool creates a SessionClearTool with the given clear callback.
func NewSessionClearTool(clear func(ctx context.Context, sessionKey, message string) error) *SessionClearTool {
	return &SessionClearTool{clear: clear}
}

func (t *SessionClearTool) Name() string          { return "session_clear" }
func (t *SessionClearTool) IsSessionScoped() bool { return true }

func (t *SessionClearTool) Description() string {
	return "Clear your active conversation and start fresh. Long-term memory is preserved — " +
		"past messages stay retrievable via session_messages / session_search and past summaries " +
		"via session_summary_get. Use between unrelated short tasks to reset working context. " +
		"Pass an optional `message` as a handoff note to your fresh self (e.g. the next task to do). " +
		"After calling this, stop and end your turn: the clear happens at the turn boundary and a new " +
		"turn begins with your handoff."
}

func (t *SessionClearTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"message": map[string]any{
				"type":        "string",
				"description": "Optional handoff note delivered to you on the fresh turn after the clear — e.g. the next task or relevant context.",
			},
		},
	}
}

func (t *SessionClearTool) Execute(ctx context.Context, args map[string]any) *tools.ToolResult {
	sessionKey := tools.ToolSessionKey(ctx)
	if sessionKey == "" {
		return tools.ErrorResult("session key not available")
	}
	if t.clear == nil {
		return tools.ErrorResult("clear function not configured")
	}
	message, _ := args["message"].(string)
	if err := t.clear(ctx, sessionKey, message); err != nil {
		return tools.ErrorResult(err.Error())
	}
	return &tools.ToolResult{
		ForLLM: "Context clear queued. End your turn now — your active conversation will be reset " +
			"and a new turn will begin" + func() string {
			if message != "" {
				return " with your handoff note."
			}
			return "."
		}(),
	}
}
