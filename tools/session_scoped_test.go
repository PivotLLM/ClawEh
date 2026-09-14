// ClawEh
// License: MIT

package tools_test

import (
	"testing"

	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/tools"
	toolsfiles "github.com/PivotLLM/ClawEh/tools/files"
	toolssession "github.com/PivotLLM/ClawEh/tools/session"
)

// TestSessionScopedInterface verifies that every session tool the provider
// publishes declares SessionScoped, so the MCP dispatcher injects the session
// key for each of them without a hardcoded list.
func TestSessionScopedInterface(t *testing.T) {
	defs := toolssession.GlobalProvider.RegisterTools(global.Deps{})
	if len(defs) == 0 {
		t.Fatal("session provider published no tools")
	}
	for _, def := range defs {
		if !def.SessionScoped {
			t.Errorf("session tool %q is not SessionScoped", def.Name)
		}
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
