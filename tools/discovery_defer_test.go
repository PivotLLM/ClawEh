// ClawEh
// License: MIT

package tools

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/providers"
)

// discoveryOnRegistry is a registry as an agent with discovery on builds it:
// two core tools, the two discovery tools (suite-registered, core), and
// hidden tools: Alice's server (reveal-together), Bob's server (not), and an
// MCP tool offered under a bare ExternalName.
func discoveryOnRegistry() (*ToolRegistry, *SearchTool, *ToolDetailsTool) {
	r := NewToolRegistry()
	r.Register(&mockTool{name: "file_read"})
	r.Register(&mockTool{name: "time_now"})
	search := NewSearchTool(r, 10)
	details := NewToolDetailsTool(r)
	r.RegisterSuite(search)
	r.RegisterSuite(details)
	r.RegisterHiddenGroup(newMockTool("mcp_alice_read", "Read a note from Alice's notebook"), "alice", true)
	r.RegisterHiddenGroup(newMockTool("mcp_alice_write", "Write a note to Alice's notebook"), "alice", true)
	r.RegisterHiddenGroup(newMockTool("mcp_bob_fetch", "Fetch a page for Bob"), "bob", false)
	r.RegisterHiddenGroup(newMockTool("mcp_bob_post", "Post a page for Bob"), "bob", false)
	r.RegisterHidden(&mockExternalTool{
		mockRegistryTool: *newMockTool("mcp_x_weather", "Weather forecast for a city"),
		external:         "x_weather",
	})
	return r, search, details
}

func defNames(defs []providers.ToolDefinition) []string {
	names := make([]string, 0, len(defs))
	for _, d := range defs {
		names = append(names, d.Function.Name)
	}
	return names
}

// With discovery on, every registered tool is offered on every call with the
// same definitions: core tools plain, search_tools marked as the tool-search
// tool, hidden tools deferred. Searching and asking for details change
// nothing.
func TestToProviderDefs_DiscoveryOn_FullStableFlaggedList(t *testing.T) {
	r, search, details := discoveryOnRegistry()
	ctx := context.Background()

	first := r.ToProviderDefs()
	wantNames := []string{
		"file_read", "get_tool_details", "mcp_alice_read", "mcp_alice_write",
		"mcp_bob_fetch", "mcp_bob_post", "x_weather", "search_tools", "time_now",
	}
	if got := defNames(first); !slices.Equal(got, wantNames) {
		t.Fatalf("names = %v, want %v (every tool, sorted by internal name)", got, wantNames)
	}

	core := map[string]bool{"file_read": true, "time_now": true, "get_tool_details": true}
	for _, d := range first {
		name := d.Function.Name
		switch {
		case name == "search_tools":
			if !d.ToolSearch || d.Deferred {
				t.Errorf("search_tools: ToolSearch=%v Deferred=%v, want true/false", d.ToolSearch, d.Deferred)
			}
		case core[name]:
			if d.ToolSearch || d.Deferred {
				t.Errorf("core %s: ToolSearch=%v Deferred=%v, want false/false", name, d.ToolSearch, d.Deferred)
			}
		default:
			if !d.Deferred || d.ToolSearch {
				t.Errorf("hidden %s: Deferred=%v ToolSearch=%v, want true/false", name, d.Deferred, d.ToolSearch)
			}
		}
	}

	for _, step := range []struct {
		name string
		run  func() *ToolResult
	}{
		{"search", func() *ToolResult { return search.Execute(ctx, map[string]any{"query": "notebook note"}) }},
		{"regex search", func() *ToolResult { return search.Execute(ctx, map[string]any{"query": "bob", "regex": true}) }},
		{"details", func() *ToolResult { return details.Execute(ctx, map[string]any{"name": "mcp_alice_read"}) }},
		{"details by bare name", func() *ToolResult { return details.Execute(ctx, map[string]any{"name": "x_weather"}) }},
		{"call a hidden tool", func() *ToolResult { return r.Execute(ctx, "mcp_bob_post", nil) }},
	} {
		if res := step.run(); res.IsError {
			t.Fatalf("%s: %s", step.name, res.ForLLM)
		}
		if got := r.ToProviderDefs(); !reflect.DeepEqual(got, first) {
			t.Fatalf("after %s the definitions changed:\n got %+v\nwant %+v", step.name, got, first)
		}
	}
}

// With discovery off every tool is core and there are no discovery tools: the
// list carries no Deferred or ToolSearch flag and is exactly the plain
// definitions.
func TestToProviderDefs_DiscoveryOff_NoFlags(t *testing.T) {
	r := NewToolRegistry()
	r.Register(newMockTool("time_now", "Current time"))
	r.Register(&mockExternalTool{
		mockRegistryTool: *newMockTool("mcp_x_weather", "Weather forecast for a city"),
		external:         "x_weather",
	})
	r.Register(newMockTool("file_read", "Read a file"))

	want := []providers.ToolDefinition{
		{Type: "function", Function: providers.ToolFunctionDefinition{
			Name: "file_read", Description: "Read a file", Parameters: newMockTool("", "").Parameters(),
		}},
		{Type: "function", Function: providers.ToolFunctionDefinition{
			Name: "x_weather", Description: "Weather forecast for a city", Parameters: newMockTool("", "").Parameters(),
		}},
		{Type: "function", Function: providers.ToolFunctionDefinition{
			Name: "time_now", Description: "Current time", Parameters: newMockTool("", "").Parameters(),
		}},
	}
	for range 3 {
		if got := r.ToProviderDefs(); !reflect.DeepEqual(got, want) {
			t.Fatalf("defs =\n %+v\nwant\n %+v", got, want)
		}
	}
}

// A registered tool runs whether hidden or not, with no search first; an
// unknown name is still not found.
func TestDiscovery_HiddenToolExecutesWithoutSearch(t *testing.T) {
	r, _, _ := discoveryOnRegistry()
	ctx := context.Background()

	for _, name := range []string{"mcp_bob_fetch", "x_weather", "mcp_x_weather"} {
		if res := r.Execute(ctx, name, nil); res.IsError {
			t.Errorf("hidden %s: %s, want it run", name, res.ForLLM)
		}
	}
	res := r.Execute(ctx, "mcp_carol_missing", nil)
	if want := fmt.Sprintf("tool %q not found", "mcp_carol_missing"); !res.IsError || res.ForLLM != want {
		t.Errorf("unknown tool: %+v, want %q", res, want)
	}
}

// search_tools references exactly its hits, no more than max_search_results,
// each with a line of text; no hits means no references.
func TestSearchTool_ReferencesHits(t *testing.T) {
	r := NewToolRegistry()
	r.Register(newMockTool("core_match", "core match tool"))
	for i := range 10 {
		r.RegisterHidden(newMockTool(fmt.Sprintf("hidden_match_%d", i), "this has a match\nsecond line"))
	}
	search := NewSearchTool(r, 4)
	ctx := context.Background()

	for _, args := range []map[string]any{
		{"query": "match"},
		{"query": "match", "regex": true},
	} {
		res := search.Execute(ctx, args)
		if res.IsError {
			t.Fatalf("%v: %s", args, res.ForLLM)
		}
		if len(res.ToolReferences) != 4 {
			t.Errorf("%v: %d references %v, want 4 (max_search_results)", args, len(res.ToolReferences), res.ToolReferences)
		}
		for _, ref := range res.ToolReferences {
			if !strings.HasPrefix(ref, "hidden_match_") {
				t.Errorf("%v: reference %q is not a hidden hit", args, ref)
			}
			if !strings.Contains(res.ForLLM, "- "+ref+": this has a match\n") {
				t.Errorf("%v: no one-line entry for %s in %q", args, ref, res.ForLLM)
			}
		}
		if strings.Contains(res.ForLLM, "second line") {
			t.Errorf("%v: description beyond its first line in %q", args, res.ForLLM)
		}
	}

	for _, args := range []map[string]any{
		{"query": "spaceship"},
		{"query": "spaceship", "regex": true},
	} {
		res := search.Execute(ctx, args)
		if res.IsError || len(res.ToolReferences) != 0 {
			t.Errorf("%v: %+v, want no hits and no references", args, res)
		}
	}
}

// Search hits and get_tool_details references use the name the tool is
// offered under, so they match the tool list.
func TestDiscovery_ReferencesUsePublishedNames(t *testing.T) {
	r, search, details := discoveryOnRegistry()
	ctx := context.Background()
	offered := defNames(r.ToProviderDefs())

	res := search.Execute(ctx, map[string]any{"query": "weather forecast city"})
	if !slices.Contains(res.ToolReferences, "x_weather") {
		t.Fatalf("search references = %v, want x_weather", res.ToolReferences)
	}
	res2 := details.Execute(ctx, map[string]any{"name": "mcp_x_weather"})
	if want := []string{"x_weather"}; !slices.Equal(res2.ToolReferences, want) {
		t.Fatalf("details references = %v, want %v", res2.ToolReferences, want)
	}
	for _, ref := range append(res.ToolReferences, res2.ToolReferences...) {
		if !slices.Contains(offered, ref) {
			t.Errorf("reference %q is not a name in the tool list %v", ref, offered)
		}
	}
}

// get_tool_details on a member of a reveal-together group references the
// whole group, the asked-for tool first; on other tools, only that tool.
func TestToolDetails_RevealTogetherGroup(t *testing.T) {
	_, _, details := discoveryOnRegistry()
	ctx := context.Background()

	tests := []struct {
		name string
		want []string
	}{
		{"mcp_alice_write", []string{"mcp_alice_write", "mcp_alice_read"}},
		{"mcp_alice_read", []string{"mcp_alice_read", "mcp_alice_write"}},
		{"mcp_bob_fetch", []string{"mcp_bob_fetch"}},
		{"time_now", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := details.Execute(ctx, map[string]any{"name": tt.name})
			if res.IsError {
				t.Fatalf("%s", res.ForLLM)
			}
			if !slices.Equal(res.ToolReferences, tt.want) {
				t.Errorf("references = %v, want %v", res.ToolReferences, tt.want)
			}
			if !strings.Contains(res.ForLLM, `"name":"`+tt.name+`"`) {
				t.Errorf("text lacks the schema of %s: %q", tt.name, res.ForLLM)
			}
		})
	}
}

// search_tools' description depends only on the registered tools.
func TestSearchTool_DescriptionStable(t *testing.T) {
	_, search, details := discoveryOnRegistry()
	ctx := context.Background()
	before := search.Description()
	search.Execute(ctx, map[string]any{"query": "notebook"})
	details.Execute(ctx, map[string]any{"name": "mcp_bob_fetch"})
	if got := search.Description(); got != before {
		t.Errorf("description changed after discovery calls:\n got %q\nwant %q", got, before)
	}
	if !strings.Contains(before, "Discoverable tool groups: mcp, x.") {
		t.Errorf("description = %q, want the hidden namespaces listed", before)
	}
}
