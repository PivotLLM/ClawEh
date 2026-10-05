// ClawEh
// License: MIT

package agent

import (
	"context"
	"encoding/json"
	"time"

	"github.com/PivotLLM/ClawEh/agentreg"
	"github.com/PivotLLM/ClawEh/internal/audit"
	"github.com/PivotLLM/ClawEh/providers"
	"github.com/PivotLLM/ClawEh/tools"
)

// recordToolCallAudit writes one tool_call row to the audit log for a tool the
// loop just ran. Arguments go through the same redaction as the INFO log
// (tools.RedactArgs), so file contents, HTTP bodies and edit text are reduced
// to byte counts; the store caps the result at audit.MaxDetailsBytes. A no-op
// until audit.Init has run.
//
// The agent column is the agent the call was made as: a sub-agent clone's
// calls are recorded under its source (so filtering on the agent still finds
// them), with the clone named in the details ("clone": "1a2b3c4d").
func recordToolCallAudit(
	ctx context.Context,
	agent *AgentInstance,
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
		Agent:      agent.toolIdentity(),
		Session:    opts.SessionKey,
		Channel:    opts.Channel,
		Sender:     opts.SenderID,
		Tool:       tc.Name,
		Details:    toolCallAuditDetails(opts.ChatID, cloneMarker(agent), tc),
		Outcome:    outcome,
		DurationMS: elapsed.Milliseconds(),
		TurnID:     turnIDFrom(ctx),
	})
}

// cloneMarker is the short id of a clone, "" for every other agent.
func cloneMarker(a *AgentInstance) string {
	if !a.Spec.IsClone() {
		return ""
	}
	return agentreg.ShortID(a.ID)
}

// toolCallAuditDetails is the JSON details column for a tool_call row: the
// chat the call was made in and the redacted argument digest, plus the
// clone's short id when a sub-agent clone made the call.
func toolCallAuditDetails(chatID, clone string, tc providers.ToolCall) string {
	details := map[string]any{
		"chat_id": chatID,
		"args":    tools.RedactArgs(tc.Name, tc.Arguments),
	}
	if clone != "" {
		details["clone"] = clone
	}
	b, err := json.Marshal(details)
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
