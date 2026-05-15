// ClawEh
// License: MIT

package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/PivotLLM/ClawEh/pkg/memory"
)

// SessionHistoryTool implements the get_session_messages MCP tool.
// It reads messages from the session archive by sequence number.
// Session scoping uses the key injected via WithSessionKey (see base.go).
type SessionHistoryTool struct {
	sessionsDir string
}

// NewSessionHistoryTool creates a SessionHistoryTool for the given sessions directory.
func NewSessionHistoryTool(sessionsDir string) *SessionHistoryTool {
	return &SessionHistoryTool{sessionsDir: sessionsDir}
}

func (t *SessionHistoryTool) Name() string { return "get_session_messages" }

func (t *SessionHistoryTool) Description() string {
	return "Retrieve historical messages from the current session archive by sequence number. " +
		"Use when the context summary references a message you need to see in full. " +
		"Provide seq (single message) or seq_start + seq_end (inclusive range)."
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

func (t *SessionHistoryTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	sessionKey := ToolSessionKey(ctx)
	if sessionKey == "" {
		return ErrorResult("session key not available")
	}

	seqStart, seqEnd, err := parseSeqArgs(args)
	if err != nil {
		return ErrorResult(err.Error())
	}

	archivePath := filepath.Join(t.sessionsDir, archiveSanitizeKey(sessionKey)+".archive.jsonl")
	msgs, readErr := readArchiveRange(archivePath, seqStart, seqEnd)
	if readErr != nil {
		return ErrorResult(fmt.Sprintf("archive read error: %v", readErr))
	}

	if len(msgs) == 0 {
		return &ToolResult{ForLLM: "not available — message has aged out of the archive"}
	}

	var sb strings.Builder
	for _, m := range msgs {
		fmt.Fprintf(&sb, "[#%d] %s:\n%s\n\n", m.Seq, m.Role, m.Content)
	}
	return &ToolResult{ForLLM: strings.TrimRight(sb.String(), "\n")}
}

// parseSeqArgs returns the seq range from args.
// If "seq" is present it takes precedence; otherwise "seq_start"+"seq_end" are used.
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

// readArchiveRange reads StoredMessages from the archive whose Seq falls in [seqStart, seqEnd].
func readArchiveRange(path string, seqStart, seqEnd int) ([]memory.StoredMessage, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var result []memory.StoredMessage
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var msg memory.StoredMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}
		if msg.Seq >= seqStart && msg.Seq <= seqEnd {
			result = append(result, msg)
		}
	}
	return result, scanner.Err()
}
