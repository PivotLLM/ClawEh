// ClawEh
// License: MIT

package tools

import (
	"context"
	"encoding/json"
	"time"
)

// SessionInfo holds the structured data returned by get_session_info.
type SessionInfo struct {
	SessionKey          string           `json:"session_key"`
	StartedAt           *time.Time       `json:"started_at,omitempty"`
	Channel             string           `json:"channel,omitempty"`
	ContextMessageCount int              `json:"context_message_count"`
	ArchiveMinSeq       int              `json:"archive_min_seq"`
	ArchiveMaxSeq       int              `json:"archive_max_seq"`
	TotalArchived       int              `json:"total_archived"`
	SummaryCovers       *SummaryCoverage `json:"summary_covers,omitempty"`
	LastCompressedAt    *time.Time       `json:"last_compressed_at,omitempty"`
}

// SummaryCoverage describes the seq range covered by the current summary.
type SummaryCoverage struct {
	SeqStart    int        `json:"seq_start"`
	SeqEnd      int        `json:"seq_end"`
	GeneratedAt *time.Time `json:"generated_at,omitempty"`
}

// SessionInfoFunc is the callback type supplied at tool construction time.
// It must return the session info for the given session key.
type SessionInfoFunc func(ctx context.Context, sessionKey string) (*SessionInfo, error)

// SessionInfoTool implements the get_session_info MCP tool.
type SessionInfoTool struct {
	infoFn SessionInfoFunc
}

// NewSessionInfoTool creates a SessionInfoTool with the given info callback.
func NewSessionInfoTool(infoFn SessionInfoFunc) *SessionInfoTool {
	return &SessionInfoTool{infoFn: infoFn}
}

func (t *SessionInfoTool) Name() string           { return "get_session_info" }
func (t *SessionInfoTool) IsSessionScoped() bool  { return true }

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

func (t *SessionInfoTool) Execute(ctx context.Context, _ map[string]any) *ToolResult {
	sessionKey := ToolSessionKey(ctx)
	if sessionKey == "" {
		return ErrorResult("session key not available")
	}
	if t.infoFn == nil {
		return ErrorResult("session info function not configured")
	}
	info, err := t.infoFn(ctx, sessionKey)
	if err != nil {
		return ErrorResult("session info error: " + err.Error())
	}
	out, _ := json.Marshal(info)
	return &ToolResult{ForLLM: string(out)}
}
