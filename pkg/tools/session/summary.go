// ClawEh
// License: MIT

package session

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/PivotLLM/ClawEh/pkg/llmcontext"
	"github.com/PivotLLM/ClawEh/pkg/memory"
	"github.com/PivotLLM/ClawEh/pkg/tools"
)

// SessionSummaryListTool implements the session_summary_list MCP tool.
// It lists context-summary checkpoints stored in the current session archive.
// Session scoping uses the key injected via WithSessionKey (see base.go).
type SessionSummaryListTool struct {
	sessionsDir string
}

// NewSessionSummaryListTool creates a SessionSummaryListTool for the given sessions directory.
func NewSessionSummaryListTool(sessionsDir string) *SessionSummaryListTool {
	return &SessionSummaryListTool{sessionsDir: sessionsDir}
}

func (t *SessionSummaryListTool) Name() string          { return "session_summary_list" }
func (t *SessionSummaryListTool) IsSessionScoped() bool { return true }

func (t *SessionSummaryListTool) Description() string {
	return "List the context-summary checkpoints recorded for the current session. " +
		"Each entry shows its id, the covered message sequence range, when it was generated, " +
		"and the model that produced it. Use session_summary_get with an id to read the full summary. " +
		"Newest checkpoints have the highest ids."
}

func (t *SessionSummaryListTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"limit": map[string]any{
				"type":        "integer",
				"description": "Maximum number of checkpoints to return (most recent first). Omit for all.",
			},
			"offset": map[string]any{
				"type":        "integer",
				"description": "Number of most-recent checkpoints to skip before listing. Default 0.",
			},
		},
	}
}

func (t *SessionSummaryListTool) Execute(ctx context.Context, args map[string]any) *tools.ToolResult {
	sessionKey := tools.ToolSessionKey(ctx)
	if sessionKey == "" {
		return tools.ErrorResult("session key not available")
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

	metas, listErr := a.ListSummaries()
	if listErr != nil {
		if errors.Is(listErr, memory.ErrArchiveUnavailable) {
			return &tools.ToolResult{ForLLM: "archive unavailable — see server logs"}
		}
		return tools.ErrorResult(fmt.Sprintf("summary list error: %v", listErr))
	}

	if len(metas) == 0 {
		return &tools.ToolResult{ForLLM: "no context summaries recorded for this session yet"}
	}

	// Present newest first for readability.
	reversed := make([]memory.SummaryMeta, len(metas))
	for i, m := range metas {
		reversed[len(metas)-1-i] = m
	}

	offset := 0
	if o, ok := intArg(args, "offset"); ok && o > 0 {
		offset = int(o)
	}
	if offset >= len(reversed) {
		return &tools.ToolResult{ForLLM: "no context summaries in the requested range"}
	}
	reversed = reversed[offset:]

	if l, ok := intArg(args, "limit"); ok && l > 0 && int(l) < len(reversed) {
		reversed = reversed[:l]
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "%d context summary checkpoint(s) (newest first):\n", len(reversed))
	for _, m := range reversed {
		fmt.Fprintf(&sb, "- id %d: covers #%d-#%d, generated %s",
			m.ID, m.CoveredSeqStart, m.CoveredSeqEnd,
			m.GeneratedAt.Format("2006-01-02 15:04 UTC"))
		if m.Model != "" {
			fmt.Fprintf(&sb, ", model %s", m.Model)
		}
		if m.Profile != "" {
			fmt.Fprintf(&sb, ", profile %s", m.Profile)
		}
		sb.WriteString("\n")
	}
	return &tools.ToolResult{ForLLM: strings.TrimRight(sb.String(), "\n")}
}

// SessionSummaryGetTool implements the session_summary_get MCP tool.
// It returns the full rendered body of one stored context-summary checkpoint.
// Session scoping uses the key injected via WithSessionKey (see base.go).
type SessionSummaryGetTool struct {
	sessionsDir string
}

// NewSessionSummaryGetTool creates a SessionSummaryGetTool for the given sessions directory.
func NewSessionSummaryGetTool(sessionsDir string) *SessionSummaryGetTool {
	return &SessionSummaryGetTool{sessionsDir: sessionsDir}
}

func (t *SessionSummaryGetTool) Name() string          { return "session_summary_get" }
func (t *SessionSummaryGetTool) IsSessionScoped() bool { return true }

func (t *SessionSummaryGetTool) Description() string {
	return "Retrieve the full text of one context-summary checkpoint for the current session by id. " +
		"Use session_summary_list to discover available ids. " +
		"Returns the summary rendered as Markdown (goals, progress, pending, constraints, key moments) " +
		"with the message sequence references it cites."
}

func (t *SessionSummaryGetTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id": map[string]any{
				"type":        "integer",
				"description": "The checkpoint id to retrieve (from session_summary_list).",
			},
		},
		"required": []string{"id"},
	}
}

func (t *SessionSummaryGetTool) Execute(ctx context.Context, args map[string]any) *tools.ToolResult {
	sessionKey := tools.ToolSessionKey(ctx)
	if sessionKey == "" {
		return tools.ErrorResult("session key not available")
	}

	id, ok := intArg(args, "id")
	if !ok {
		return tools.ErrorResult("id parameter is required")
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

	rec, found, getErr := a.GetSummary(id)
	if getErr != nil {
		if errors.Is(getErr, memory.ErrArchiveUnavailable) {
			return &tools.ToolResult{ForLLM: "archive unavailable — see server logs"}
		}
		return tools.ErrorResult(fmt.Sprintf("summary get error: %v", getErr))
	}
	if !found {
		return &tools.ToolResult{ForLLM: fmt.Sprintf("no context summary with id %d", id)}
	}

	// Render the stored JSON body as readable Markdown. The covered seq range
	// bounds the rendered references; an unparseable body falls back to raw text.
	rendered := llmcontext.RenderSummaryFromRaw(rec.Summary, rec.CoveredSeqStart, rec.CoveredSeqEnd)

	var sb strings.Builder
	fmt.Fprintf(&sb, "Context summary checkpoint id %d (covers #%d-#%d, generated %s",
		rec.ID, rec.CoveredSeqStart, rec.CoveredSeqEnd,
		rec.GeneratedAt.Format("2006-01-02 15:04 UTC"))
	if rec.Model != "" {
		fmt.Fprintf(&sb, ", model %s", rec.Model)
	}
	if rec.Profile != "" {
		fmt.Fprintf(&sb, ", profile %s", rec.Profile)
	}
	sb.WriteString(")\n\n")
	sb.WriteString(rendered)
	return &tools.ToolResult{ForLLM: sb.String()}
}
