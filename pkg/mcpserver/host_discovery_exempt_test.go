// ClawEh
// License: MIT

package mcpserver

import (
	"context"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/mcpserver/acl"
	"github.com/PivotLLM/ClawEh/pkg/tools"
	"github.com/mark3labs/mcp-go/server"
)

// regWithHiddenSuiteTool returns a registry holding one suite tool registered
// hidden + TTL-gated — exactly how the maestro/fusion suites register when
// progressive discovery is enabled (RegisterSuiteHidden ⇒ IsCore=false, TTL=0).
func regWithHiddenSuiteTool(tool tools.Tool) *tools.ToolRegistry {
	r := tools.NewToolRegistry()
	r.RegisterSuiteHidden(tool)
	return r
}

// TestHost_ListsAndDispatchesTTLHiddenSuiteTool is the regression for the Penny
// bug: a claude-cli agent reaching a maestro tool through the MCP host got
// "tool_not_in_registry" because the tool was discovery-hidden (TTL 0). The host
// must never apply progressive discovery — it lists the tool AND dispatches it,
// authorization coming from the ACL policy, not the TTL state.
func TestHost_ListsAndDispatchesTTLHiddenSuiteTool(t *testing.T) {
	tool := &mockTool{name: "maestro_file_list", params: map[string]any{}, result: tools.NewToolResult("ok")}
	reg := regWithHiddenSuiteTool(tool)

	// Sanity: the plain (in-loop) Get gates it out while hidden, but GetForHost
	// (the host path) resolves it. This is the crux of the fix.
	if _, ok := reg.Get("maestro_file_list"); ok {
		t.Fatal("precondition: a TTL-hidden tool must be gated by the in-loop Get")
	}
	if _, ok := reg.GetForHost("maestro_file_list"); !ok {
		t.Fatal("GetForHost must resolve a TTL-hidden tool for the host path")
	}

	regs := map[string]*tools.ToolRegistry{"penny": reg}
	st, tok := seedSessionToken("penny")
	resolver := resolverFor(regs)

	// (a) tools/list on the host includes the hidden suite tool.
	srv := server.NewMCPServer("t", "0")
	addToolsToServer(srv, bearerAuthMode, regs, []string{"*"}, st, resolver, nil, acl.Default, nil, nil)
	if _, ok := srv.ListTools()["maestro_file_list"]; !ok {
		t.Fatal("host catalogue must list the discovery-hidden suite tool")
	}

	// (b) dispatch succeeds instead of returning tool_not_in_registry.
	out, isErr := dispatchToolCall(context.Background(), "maestro_file_list",
		map[string]any{"session_token": tok}, st, resolver, nil, acl.Default, nil)
	if isErr {
		t.Fatalf("host dispatch of a hidden suite tool must succeed, got error: %s", out)
	}
	if tool.calls != 1 {
		t.Fatalf("expected the tool to execute once, got %d calls", tool.calls)
	}
}
