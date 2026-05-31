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

// SessionHistorySearchTool implements the session_search MCP tool.
// It searches archived session messages using FTS5 full-text search.
// Session scoping uses the key injected via WithSessionKey (see base.go).
type SessionHistorySearchTool struct {
	sessionsDir string
}

// NewSessionHistorySearchTool creates a SessionHistorySearchTool for the given sessions directory.
func NewSessionHistorySearchTool(sessionsDir string) *SessionHistorySearchTool {
	return &SessionHistorySearchTool{sessionsDir: sessionsDir}
}

func (t *SessionHistorySearchTool) Name() string          { return "session_search" }
func (t *SessionHistorySearchTool) IsSessionScoped() bool { return true }

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

func (t *SessionHistorySearchTool) Execute(ctx context.Context, args map[string]any) *tools.ToolResult {
	sessionKey := tools.ToolSessionKey(ctx)
	if sessionKey == "" {
		return tools.ErrorResult("session key not available")
	}

	query, ok := args["query"].(string)
	if !ok || strings.TrimSpace(query) == "" {
		return tools.ErrorResult("query parameter is required")
	}

	role, _ := args["role"].(string)

	limit := 20
	if l, ok := intArg(args, "limit"); ok {
		limit = int(l)
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
			return &tools.ToolResult{ForLLM: "archive unavailable — see server logs"}
		}
		return tools.ErrorResult(fmt.Sprintf("archive open error: %v", openErr))
	}
	defer a.Close()

	results, searchErr := a.Search(ctx, query, role, limit)
	if searchErr != nil {
		if errors.Is(searchErr, memory.ErrArchiveUnavailable) {
			return &tools.ToolResult{ForLLM: "archive unavailable — see server logs"}
		}
		return tools.ErrorResult(fmt.Sprintf("search error: %v", searchErr))
	}

	if len(results) == 0 {
		return &tools.ToolResult{ForLLM: "no matching messages found"}
	}

	type msgEntry struct {
		Seq       int64     `json:"seq"`
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
	return &tools.ToolResult{ForLLM: string(out)}
}
