// ClawEh
// License: MIT

package agent

import (
	"context"
	"encoding/json"
	"time"

	"github.com/PivotLLM/ClawEh/internal/audit"
	"github.com/PivotLLM/ClawEh/providers"
	"github.com/PivotLLM/ClawEh/tools"
)

// recordToolCallAudit writes one tool_call row to the audit log for a tool the
// loop just ran. Arguments go through the same redaction as the INFO log
// (tools.RedactArgs), so file contents, HTTP bodies and edit text are reduced
// to byte counts; the store caps the result at audit.MaxDetailsBytes. A no-op
// until audit.Init has run.
func recordToolCallAudit(
	ctx context.Context,
	agentID string,
	opts processOptions,
	tc providers.ToolCall,
	result *tools.ToolResult,
	elapsed time.Duration,
) {
	store := audit.Default()
	if store == nil {
		return
	}
	outcome := audit.OutcomeOK
	if result == nil || result.IsError || result.Err != nil {
		outcome = audit.OutcomeError
	}
	store.Record(audit.Event{
		Kind:       audit.KindToolCall,
		Agent:      agentID,
		Session:    opts.SessionKey,
		Channel:    opts.Channel,
		Sender:     opts.SenderID,
		Tool:       tc.Name,
		Details:    toolCallAuditDetails(opts.ChatID, tc),
		Outcome:    outcome,
		DurationMS: elapsed.Milliseconds(),
		TurnID:     turnIDFrom(ctx),
	})
}

// toolCallAuditDetails is the JSON details column for a tool_call row: the
// chat the call was made in and the redacted argument digest.
func toolCallAuditDetails(chatID string, tc providers.ToolCall) string {
	b, err := json.Marshal(map[string]any{
		"chat_id": chatID,
		"args":    tools.RedactArgs(tc.Name, tc.Arguments),
	})
	if err != nil {
		return `{"chat_id":` + jsonString(chatID) + `,"args":"<unmarshalable>"}`
	}
	return string(b)
}

func jsonString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}
