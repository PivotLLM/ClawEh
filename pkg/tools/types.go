package tools

import (
	"context"
	"time"
)

type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type ToolCall struct {
	ID        string         `json:"id"`
	Type      string         `json:"type"`
	Function  *FunctionCall  `json:"function,omitempty"`
	Name      string         `json:"name,omitempty"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type LLMResponse struct {
	Content      string     `json:"content"`
	ToolCalls    []ToolCall `json:"tool_calls,omitempty"`
	FinishReason string     `json:"finish_reason"`
	Usage        *UsageInfo `json:"usage,omitempty"`
}

type UsageInfo struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type LLMProvider interface {
	Chat(
		ctx context.Context,
		messages []Message,
		tools []ToolDefinition,
		model string,
		options map[string]any,
	) (*LLMResponse, error)
	GetDefaultModel() string
}

type ToolDefinition struct {
	Type     string                 `json:"type"`
	Function ToolFunctionDefinition `json:"function"`
}

type ToolFunctionDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// SessionInfo holds the structured data returned by the session_info tool.
type SessionInfo struct {
	SessionKey          string           `json:"session_key"`
	StartedAt           *time.Time       `json:"started_at,omitempty"`
	Channel             string           `json:"channel,omitempty"`
	ContextMessageCount int              `json:"context_message_count"`
	ArchiveMinSeq       int64            `json:"archive_min_seq"`
	ArchiveMaxSeq       int64            `json:"archive_max_seq"`
	TotalArchived       int64            `json:"total_archived"`
	SummaryCovers       *SummaryCoverage `json:"summary_covers,omitempty"`
	LastCompressedAt    *time.Time       `json:"last_compressed_at,omitempty"`
}

// SummaryCoverage describes the seq range covered by the current summary.
type SummaryCoverage struct {
	SeqStart    int64      `json:"seq_start"`
	SeqEnd      int64      `json:"seq_end"`
	GeneratedAt *time.Time `json:"generated_at,omitempty"`
}
