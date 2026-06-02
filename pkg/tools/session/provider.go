package session

import (
	"path/filepath"

	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/tools"
)

// Provider is the singleton ToolProvider for session tools.
var Provider sessionProvider

type sessionProvider struct{}

func (p sessionProvider) Namespace() string   { return "session" }
func (p sessionProvider) Description() string { return "Session history and management tools" }
func (p sessionProvider) Category() string    { return "session" }
func (p sessionProvider) ConfigKey() string   { return "session" }

func (p sessionProvider) Available(cfg *config.Config) (bool, string) {
	return true, ""
}

func (p sessionProvider) Describe() []tools.ToolDescriptor {
	return []tools.ToolDescriptor{
		{Name: "session_messages", Description: "Retrieve archived session messages by sequence number range.", Category: "context", ConfigKey: "session_messages", DefaultEnabled: true},
		{Name: "session_search", Description: "Full-text search over archived session messages.", Category: "context", ConfigKey: "session_search", DefaultEnabled: true},
		{Name: "session_compact", Description: "Trigger an immediate context compaction for the current session.", Category: "context", ConfigKey: "session_compact", DefaultEnabled: true},
		{Name: "session_info", Description: "Return archive bounds, message count, and summary coverage.", Category: "context", ConfigKey: "session_info", DefaultEnabled: true},
		{Name: "session_summary_list", Description: "List recorded context-summary checkpoints for the current session.", Category: "context", ConfigKey: "session_summary_list", DefaultEnabled: true},
		{Name: "session_summary_get", Description: "Retrieve the full text of one context-summary checkpoint by id.", Category: "context", ConfigKey: "session_summary_get", DefaultEnabled: true},
	}
}

func (p sessionProvider) Build(deps tools.ToolDeps) []tools.Tool {
	cfg := deps.Cfg
	if cfg == nil {
		return nil
	}

	var result []tools.Tool

	// Archive-based tools: session_messages, session_search
	sessionsDir := filepath.Join(deps.Workspace, "sessions")
	result = append(result, NewSessionHistoryTool(sessionsDir))
	result = append(result, NewSessionHistorySearchTool(sessionsDir))
	result = append(result, NewSessionSummaryListTool(sessionsDir))
	result = append(result, NewSessionSummaryGetTool(sessionsDir))

	// Closure-based tools: session_compact, session_info
	// These require CompactFn/SessionInfoFn — return nil slices when not provided (phase 1 build).
	if deps.CompactFn != nil {
		result = append(result, NewSessionCompactTool(deps.CompactFn))
	}
	if deps.SessionInfoFn != nil {
		result = append(result, NewSessionInfoTool(SessionInfoFunc(deps.SessionInfoFn)))
	}

	return result
}
