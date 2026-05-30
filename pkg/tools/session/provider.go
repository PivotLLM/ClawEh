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
