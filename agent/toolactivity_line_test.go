package agent

import "testing"

// ToolActivityLine renders the MCP-path breadcrumb only when /tools is on for the
// session, and returns "" for an unknown agent. This is the callback wired into
// the MCP server so CLI-provider tool calls surface like loop-dispatched ones.
func TestToolActivityLine(t *testing.T) {
	al, _, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()

	agentInstance := al.registry.GetDefaultAgent()
	if agentInstance == nil {
		t.Fatal("no default agent")
	}
	const sessionKey = "tools-line"

	// Off (default): no breadcrumb.
	if got := al.ToolActivityLine(agentInstance.ID, sessionKey, "file_write", nil); got != "" {
		t.Errorf("tools off: expected empty line, got %q", got)
	}

	// On: renders the same summary the loop uses.
	al.setShowToolActivity(agentInstance, sessionKey, true)
	got := al.ToolActivityLine(agentInstance.ID, sessionKey, "file_write", map[string]any{"path": "notes.md"})
	if got == "" {
		t.Fatal("tools on: expected a breadcrumb line, got empty")
	}
	if want := toolActivitySummary("file_write", map[string]any{"path": "notes.md"}); got != want {
		t.Errorf("line = %q; want %q (parity with the loop renderer)", got, want)
	}

	// Unknown agent: never panics, returns "".
	if got := al.ToolActivityLine("no-such-agent", sessionKey, "file_write", nil); got != "" {
		t.Errorf("unknown agent: expected empty line, got %q", got)
	}
}
