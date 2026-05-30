// ClawEh
// License: MIT

package tools_test

import (
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/tools"
	toolsfiles "github.com/PivotLLM/ClawEh/pkg/tools/files"
	toolssession "github.com/PivotLLM/ClawEh/pkg/tools/session"
)

// TestSessionScopedInterface verifies that all four session tools implement
// the SessionScoped interface and return true from IsSessionScoped.
// This ensures the MCP dispatcher automatically injects the session key
// for every tool that calls ToolSessionKey(ctx), without a hardcoded list.
func TestSessionScopedInterface(t *testing.T) {
	testCases := []struct {
		name string
		tool tools.Tool
	}{
		{"session_history", toolssession.NewSessionHistoryTool("")},
		{"session_history_search", toolssession.NewSessionHistorySearchTool("")},
		{"session_compact", toolssession.NewSessionCompactTool(nil)},
		{"session_info", toolssession.NewSessionInfoTool(nil)},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ss, ok := tc.tool.(tools.SessionScoped)
			if !ok {
				t.Fatalf("%s does not implement SessionScoped", tc.name)
			}
			if !ss.IsSessionScoped() {
				t.Errorf("%s.IsSessionScoped() = false, want true", tc.name)
			}
		})
	}
}

// TestNonSessionToolDoesNotImplementSessionScoped confirms that a tool that
// does not call ToolSessionKey should not implement SessionScoped. This is a
// representative spot-check — the registry never enforces the interface, but
// it documents the expected boundary.
func TestNonSessionToolDoesNotImplementSessionScoped(t *testing.T) {
	// ReadFileTool is a typical non-session tool. It must not accidentally
	// satisfy SessionScoped; if it does, that is an implementation mistake.
	var tool tools.Tool = toolsfiles.NewReadFileTool("", false, 0)
	if _, ok := tool.(tools.SessionScoped); ok {
		t.Errorf("ReadFileTool must not implement SessionScoped")
	}
}
