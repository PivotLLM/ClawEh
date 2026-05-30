// ClawEh
// License: MIT

package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/memory"
	"github.com/PivotLLM/ClawEh/pkg/tools"
)

// SessionHistoryTool implements the session_messages MCP tool.
// It reads messages from the session archive by sequence number.
// Session scoping uses the key injected via WithSessionKey (see base.go).
type SessionHistoryTool struct {
	sessionsDir string
}

// NewSessionHistoryTool creates a SessionHistoryTool for the given sessions directory.
func NewSessionHistoryTool(sessionsDir string) *SessionHistoryTool {
	return &SessionHistoryTool{sessionsDir: sessionsDir}
}

func (t *SessionHistoryTool) Name() string          { return "session_messages" }
func (t *SessionHistoryTool) IsSessionScoped() bool { return true }

func (t *SessionHistoryTool) Description() string {
	return "Retrieve historical messages from the current session archive by sequence number. " +
		"Returns messages in the requested seq range, capped to the most recent 5000. " +
		"Request smaller ranges for efficiency. " +
		"Use when the context summary references a seq number and you need the full message content."
}

func (t *SessionHistoryTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"seq": map[string]any{
				"type":        "integer",
				"description": "Single message sequence number. Takes precedence over seq_start/seq_end.",
			},
			"seq_start": map[string]any{
				"type":        "integer",
				"description": "Start of sequence range (inclusive). Used when seq is absent.",
			},
			"seq_end": map[string]any{
				"type":        "integer",
				"description": "End of sequence range (inclusive). Used when seq is absent.",
			},
		},
	}
}

func (t *SessionHistoryTool) Execute(ctx context.Context, args map[string]any) *tools.ToolResult {
	sessionKey := tools.ToolSessionKey(ctx)
	if sessionKey == "" {
		return tools.ErrorResult("session key not available")
	}

	seqStart, seqEnd, err := parseSeqArgs(args)
	if err != nil {
		return tools.ErrorResult(err.Error())
	}

	archivePath := filepath.Join(t.sessionsDir, archiveSanitizeKey(sessionKey)+".archive.db")
	a, openErr := memory.OpenReadOnly(archivePath)
	if openErr != nil {
		if errors.Is(openErr, memory.ErrArchiveUnavailable) {
			return &tools.ToolResult{ForLLM: "archive unavailable — see server logs"}
		}
		return tools.ErrorResult(fmt.Sprintf("archive open error: %v", openErr))
	}
	defer a.Close()

	const windowSize = 5000
	_, maxSeq, boundsErr := a.Bounds()
	if boundsErr != nil {
		if errors.Is(boundsErr, memory.ErrArchiveUnavailable) {
			return &tools.ToolResult{ForLLM: "archive unavailable — see server logs"}
		}
		return tools.ErrorResult(fmt.Sprintf("archive bounds error: %v", boundsErr))
	}

	if maxSeq == 0 {
		return &tools.ToolResult{ForLLM: "not available in the current archive window"}
	}

	effectiveMin := seqStart
	if floor := maxSeq - windowSize + 1; floor > effectiveMin {
		effectiveMin = floor
	}
	effectiveMax := seqEnd
	if maxSeq < effectiveMax {
		effectiveMax = maxSeq
	}

	msgs, readErr := a.QueryRange(effectiveMin, effectiveMax)
	if readErr != nil {
		if errors.Is(readErr, memory.ErrArchiveUnavailable) {
			return &tools.ToolResult{ForLLM: "archive unavailable — see server logs"}
		}
		return tools.ErrorResult(fmt.Sprintf("archive read error: %v", readErr))
	}

	if len(msgs) == 0 {
		return &tools.ToolResult{ForLLM: "not available in the current archive window"}
	}

	type toolResultEntry struct {
		seq     int
		content string
		status  string
	}
	toolResults := make(map[string]toolResultEntry)
	for _, m := range msgs {
		if m.Role == "tool" && m.ToolCallID != "" {
			toolResults[m.ToolCallID] = toolResultEntry{
				seq:     m.Seq,
				content: m.Content,
				status:  "success",
			}
		}
	}

	type toolCallEntry struct {
		Name   string `json:"name"`
		Input  string `json:"input"`
		Output string `json:"output,omitempty"`
		Status string `json:"status"`
		Seq    int    `json:"result_seq,omitempty"`
	}
	type msgEntry struct {
		Seq       int             `json:"seq"`
		Role      string          `json:"role"`
		Source    string          `json:"source,omitempty"`
		Content   string          `json:"content"`
		CreatedAt time.Time       `json:"created_at"`
		ToolCalls []toolCallEntry `json:"tool_calls,omitempty"`
	}
	entries := make([]msgEntry, len(msgs))
	for i, m := range msgs {
		e := msgEntry{
			Seq:       m.Seq,
			Role:      m.Role,
			Source:    m.Source,
			Content:   m.Content,
			CreatedAt: m.CreatedAt,
		}
		for _, tc := range m.ToolCalls {
			tce := toolCallEntry{
				Name:   tc.Name,
				Input:  "",
				Status: "pending",
			}
			if tc.Function != nil {
				tce.Input = tc.Function.Arguments
			}
			if res, ok := toolResults[tc.ID]; ok {
				tce.Output = res.content
				tce.Status = res.status
				tce.Seq = res.seq
			}
			e.ToolCalls = append(e.ToolCalls, tce)
		}
		entries[i] = e
	}
	out, _ := json.Marshal(entries)
	return &tools.ToolResult{ForLLM: string(out)}
}

// parseSeqArgs returns the seq range from args.
func parseSeqArgs(args map[string]any) (seqStart, seqEnd int, err error) {
	if seq, ok := intArg(args, "seq"); ok {
		return seq, seq, nil
	}
	start, hasStart := intArg(args, "seq_start")
	end, hasEnd := intArg(args, "seq_end")
	if !hasStart || !hasEnd {
		return 0, 0, fmt.Errorf("seq or seq_start+seq_end required")
	}
	if start > end {
		return 0, 0, fmt.Errorf("seq_start (%d) > seq_end (%d)", start, end)
	}
	return start, end, nil
}

func intArg(args map[string]any, key string) (int, bool) {
	v, ok := args[key]
	if !ok || v == nil {
		return 0, false
	}
	switch n := v.(type) {
	case int:
		return n, true
	case float64:
		return int(n), true
	case int64:
		return int(n), true
	case json.Number:
		i, e := n.Int64()
		return int(i), e == nil
	}
	return 0, false
}

// archiveSanitizeKey converts a session key to a safe filename.
// Must match sanitizeKey in pkg/memory/jsonl.go.
func archiveSanitizeKey(key string) string {
	s := strings.ReplaceAll(key, ":", "_")
	s = strings.ReplaceAll(s, "/", "_")
	s = strings.ReplaceAll(s, "\\", "_")
	return s
}
