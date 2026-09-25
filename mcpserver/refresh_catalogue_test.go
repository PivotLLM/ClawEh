// ClawEh
// License: MIT

package mcpserver

import (
	"slices"
	"testing"

	"github.com/PivotLLM/ClawEh/tools"
)

// RefreshCatalogue adds a tool that appeared in an agent registry, deletes one
// that disappeared, and replaces one whose definition changed — on both
// endpoints, as seen through the mcp-go server's tools/list.
func TestRefreshCatalogue_FollowsRegistryChanges(t *testing.T) {
	readFile := &mockTool{name: "read_file", desc: "v1", params: map[string]any{}, result: tools.NewToolResult("a")}
	writeFile := &mockTool{name: "write_file", params: map[string]any{}, result: tools.NewToolResult("a")}
	reg := newRegistryWith(readFile, writeFile)

	srv, err := New(
		WithAgentRegistries(map[string]*tools.ToolRegistry{"alice": reg}),
		WithAllowlist([]string{"*"}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := listedToolNames(t, srv, nil), []string{"read_file", "write_file"}; !slices.Equal(got, want) {
		t.Fatalf("initial catalogue = %v, want %v", got, want)
	}

	// A tool appears, one disappears, and one is re-registered with a new
	// description (the way an unchanged upstream MCP tool is refreshed).
	reg.Register(&mockTool{name: "list_dir", params: map[string]any{}, result: tools.NewToolResult("a")})
	reg.RemoveByPrefix("write_file")
	reg.Register(&mockTool{name: "read_file", desc: "v2", params: map[string]any{}, result: tools.NewToolResult("a")})

	srv.RefreshCatalogue()

	if got, want := listedToolNames(t, srv, nil), []string{"list_dir", "read_file"}; !slices.Equal(got, want) {
		t.Fatalf("catalogue after refresh = %v, want %v", got, want)
	}
	if desc := srv.srv.ListTools()["read_file"].Tool.Description; desc != "v2" {
		t.Errorf("read_file description after refresh = %q, want v2", desc)
	}

	// The bearer endpoint follows the same registries.
	for _, ep := range srv.endpoints {
		listed := ep.srv.ListTools()
		if _, ok := listed["list_dir"]; !ok {
			t.Error("bearer endpoint should publish the added tool")
		}
		if _, ok := listed["write_file"]; ok {
			t.Error("bearer endpoint should no longer publish the removed tool")
		}
	}

	// A refresh with nothing changed registers nothing and removes nothing.
	for _, ep := range srv.endpoints {
		if added, deleted := ep.refresh(srv.agentRegistries); added != 0 || deleted != 0 {
			t.Errorf("no-op refresh reported added=%d deleted=%d", added, deleted)
		}
	}
}
