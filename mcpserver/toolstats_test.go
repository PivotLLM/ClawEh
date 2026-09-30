// ClawEh
// License: MIT

package mcpserver

import (
	"context"
	"testing"

	"github.com/PivotLLM/ClawEh/tools"
)

// TestDispatch_TalliesSubagentToolCalls: a verified MCP tool call made under a
// sub-agent session is counted in that run's tool tally (CLI providers reach
// their tools this way), failures included.
func TestDispatch_TalliesSubagentToolCalls(t *testing.T) {
	const session = "agent:alice:subagent:tally"
	ok := &mockTool{name: "read_file", params: map[string]any{}, result: &tools.ToolResult{ForLLM: "contents"}}
	bad := &mockTool{name: "write_file", params: map[string]any{}, result: &tools.ToolResult{ForLLM: "disk full", IsError: true}}
	regs := map[string]*tools.ToolRegistry{"alice": newRegistryWith(ok, bad)}
	st := newSessionTokenStore()
	tok := st.Issue("alice", session, "/ws/alice/sessions")

	tools.BeginToolStats(session)
	defer tools.EndToolStats(session)
	for _, name := range []string{"read_file", "write_file"} {
		dispatchToolCall(context.Background(), name,
			map[string]any{"session_token": tok}, st, resolverFor(regs), nil, nil, nil, nil)
	}

	calls, errs, last := tools.EndToolStats(session)
	if calls != 2 || errs != 1 || last != "write_file: disk full" {
		t.Errorf("tally = %d calls, %d errors, last %q; want 2, 1, %q", calls, errs, last, "write_file: disk full")
	}
}
