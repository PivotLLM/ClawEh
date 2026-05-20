// ClawEh
// License: MIT

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/memory"
)

// SessionHistorySearchTool implements the search_session_messages MCP tool.
// It searches archived session messages using FTS5 full-text search.
// Session scoping uses the key injected via WithSessionKey (see base.go).
type SessionHistorySearchTool struct {
	sessionsDir string
}

// NewSessionHistorySearchTool creates a SessionHistorySearchTool for the given sessions directory.
func NewSessionHistorySearchTool(sessionsDir string) *SessionHistorySearchTool {
	return &SessionHistorySearchTool{sessionsDir: sessionsDir}
}

func (t *SessionHistorySearchTool) Name() string { return "search_session_messages" }

func (t *SessionHistorySearchTool) Description() string {
	return "Search archived session messages using FTS5 full-text search. " +
		"FTS5 syntax examples: cat AND dog, \"exact phrase\", cat OR dog, NOT cat, cat*, NEAR(cat dog, 5). " +
		"Default 20 results, maximum 100. " +
		"Role filter values: \"user\", \"assistant\", \"tool\". Omit for all roles. " +
		"Use when you need to find past messages by topic rather than by sequence number. " +
		"Maximum query length: 500 characters."
}

func (t *SessionHistorySearchTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "FTS5 search expression. Supports AND, OR, NOT, phrase queries (\"exact phrase\"), prefix (word*), and NEAR(cat dog, 5). Maximum 500 characters.",
			},
			"role": map[string]any{
				"type":        "string",
				"description": "Filter results by role: \"user\", \"assistant\", or \"tool\". Omit to return all roles.",
				"enum":        []string{"user", "assistant", "tool"},
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Maximum number of results to return. Default 20, maximum 100.",
			},
		},
		"required": []string{"query"},
	}
}

func (t *SessionHistorySearchTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	sessionKey := ToolSessionKey(ctx)
	if sessionKey == "" {
		return ErrorResult("session key not available")
	}

	query, ok := args["query"].(string)
	if !ok || strings.TrimSpace(query) == "" {
		return ErrorResult("query parameter is required")
	}

	role, _ := args["role"].(string)

	limit := 20
	if l, ok := intArg(args, "limit"); ok {
		limit = l
	}
	if limit < 1 {
		limit = 1
	}
	if limit > 100 {
		limit = 100
	}

	archivePath := filepath.Join(t.sessionsDir, archiveSanitizeKey(sessionKey)+".archive.db")
	a, openErr := memory.OpenReadOnly(archivePath)
	if openErr != nil {
		if errors.Is(openErr, memory.ErrArchiveUnavailable) {
			return &ToolResult{ForLLM: "archive unavailable — see server logs"}
		}
		return ErrorResult(fmt.Sprintf("archive open error: %v", openErr))
	}
	defer a.Close()

	results, searchErr := a.Search(ctx, query, role, limit)
	if searchErr != nil {
		if errors.Is(searchErr, memory.ErrArchiveUnavailable) {
			return &ToolResult{ForLLM: "archive unavailable — see server logs"}
		}
		// FTS5 parse errors and other SQL errors are surfaced as tool errors.
		// The query is a parameter value (never interpolated), so this is safe.
		return ErrorResult(fmt.Sprintf("search error: %v", searchErr))
	}

	if len(results) == 0 {
		return &ToolResult{ForLLM: "no matching messages found"}
	}

	type msgEntry struct {
		Seq       int       `json:"seq"`
		Role      string    `json:"role"`
		Source    string    `json:"source,omitempty"`
		Content   string    `json:"content"`
		CreatedAt time.Time `json:"created_at"`
	}
	entries := make([]msgEntry, len(results))
	for i, r := range results {
		entries[i] = msgEntry{
			Seq:       r.Seq,
			Role:      r.Message.Role,
			Source:    r.Message.Source,
			Content:   r.Message.Content,
			CreatedAt: r.CreatedAt,
		}
	}
	out, _ := json.Marshal(entries)
	return &ToolResult{ForLLM: string(out)}
}
