// ClawEh
// License: MIT

package tools

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

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

	var sb strings.Builder
	for _, r := range results {
		msgRole := r.Message.Role
		if r.Message.Source != "" {
			msgRole = r.Message.Role + " [" + r.Message.Source + "]"
		}
		fmt.Fprintf(&sb, "[#%d] %s:\n%s\n\n", r.Seq, msgRole, r.Message.Content)
	}
	return &ToolResult{ForLLM: strings.TrimRight(sb.String(), "\n")}
}
