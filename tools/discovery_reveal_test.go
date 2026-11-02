package tools

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"
)

// providerDefNames returns the names ToProviderDefs advertises, sorted.
func providerDefNames(r *ToolRegistry) []string {
	defs := r.ToProviderDefs()
	names := make([]string, 0, len(defs))
	for _, d := range defs {
		names = append(names, d.Function.Name)
	}
	slices.Sort(names)
	return names
}

// discoveryRegistry holds two core tools (alice_core, bob_core), two ungrouped
// hidden tools (alice_hidden, bob_hidden) and the get_tool_details tool.
func discoveryRegistry(t *testing.T) (*ToolRegistry, *ToolDetailsTool) {
	t.Helper()
	r := NewToolRegistry()
	r.Register(&mockTool{name: "alice_core"})
	r.Register(&mockTool{name: "bob_core"})
	r.RegisterHidden(&mockTool{name: "alice_hidden"})
	r.RegisterHidden(&mockTool{name: "bob_hidden"})
	return r, NewToolDetailsTool(r)
}

func reveal(t *testing.T, details *ToolDetailsTool, name string) {
	t.Helper()
	if res := details.Execute(context.Background(), map[string]any{"name": name}); res.IsError {
		t.Fatalf("get_tool_details(%q): %s", name, res.ForLLM)
	}
}

// A revealed tool stays in the tool list and callable however many dispatches,
// iterations and tool calls follow: nothing expires and nothing is evicted.
func TestDiscovery_RevealedToolStaysVisible(t *testing.T) {
	r, details := discoveryRegistry(t)
	reveal(t, details, "alice_hidden")

	want := []string{"alice_core", "alice_hidden", "bob_core"}
	for i := range 500 {
		if got := providerDefNames(r); !slices.Equal(got, want) {
			t.Fatalf("call %d: ToProviderDefs = %v, want %v", i, got, want)
		}
		if res := r.Execute(context.Background(), "alice_hidden", nil); res.IsError {
			t.Fatalf("call %d: revealed tool not callable: %s", i, res.ForLLM)
		}
		if res := r.Execute(context.Background(), "alice_core", nil); res.IsError {
			t.Fatalf("call %d: core tool not callable: %s", i, res.ForLLM)
		}
	}

	// Revealing another tool adds it; the first stays.
	reveal(t, details, "bob_hidden")
	want = []string{"alice_core", "alice_hidden", "bob_core", "bob_hidden"}
	if got := providerDefNames(r); !slices.Equal(got, want) {
		t.Fatalf("after second reveal: ToProviderDefs = %v, want %v", got, want)
	}
}

// Revealing a member of a reveal-together group reveals the whole group; a
// group without the flag reveals only the named tool, and other tools stay
// hidden either way.
func TestDiscovery_RevealGroups(t *testing.T) {
	tests := []struct {
		name           string
		revealTogether bool
		want           []string
	}{
		{"reveal together", true, []string{"alice_a", "alice_b", "alice_c"}},
		{"independent", false, []string{"alice_a"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := NewToolRegistry()
			r.RegisterHiddenGroup(&mockTool{name: "alice_a"}, "alice", tc.revealTogether)
			r.RegisterHiddenGroup(&mockTool{name: "alice_b"}, "alice", tc.revealTogether)
			r.RegisterHiddenGroup(&mockTool{name: "alice_c"}, "alice", tc.revealTogether)
			r.RegisterHiddenGroup(&mockTool{name: "bob_a"}, "bob", true)
			r.RegisterHidden(&mockTool{name: "ungrouped"})

			reveal(t, NewToolDetailsTool(r), "alice_a")

			if got := providerDefNames(r); !slices.Equal(got, tc.want) {
				t.Fatalf("ToProviderDefs = %v, want %v", got, tc.want)
			}
		})
	}
}

// ResetRevealed hides every revealed tool again; core tools are untouched,
// a hidden tool can be revealed again afterwards, and the namespaces offered
// for discovery do not change.
func TestDiscovery_ResetRevealedHidesRevealedTools(t *testing.T) {
	r, details := discoveryRegistry(t)
	namespaces := r.HiddenNamespaces()

	reveal(t, details, "alice_hidden")
	reveal(t, details, "bob_hidden")
	if got := r.HiddenNamespaces(); !slices.Equal(got, namespaces) {
		t.Fatalf("HiddenNamespaces after reveal = %v, want %v", got, namespaces)
	}

	r.ResetRevealed()

	want := []string{"alice_core", "bob_core"}
	if got := providerDefNames(r); !slices.Equal(got, want) {
		t.Fatalf("after reset: ToProviderDefs = %v, want %v", got, want)
	}
	for _, name := range []string{"alice_hidden", "bob_hidden"} {
		if _, ok := r.Get(name); ok {
			t.Errorf("after reset: %s still callable", name)
		}
	}
	for _, name := range want {
		if _, ok := r.Get(name); !ok {
			t.Errorf("after reset: core tool %s not callable", name)
		}
	}
	if got := r.HiddenNamespaces(); !slices.Equal(got, namespaces) {
		t.Fatalf("HiddenNamespaces after reset = %v, want %v", got, namespaces)
	}

	reveal(t, details, "bob_hidden")
	want = []string{"alice_core", "bob_core", "bob_hidden"}
	if got := providerDefNames(r); !slices.Equal(got, want) {
		t.Fatalf("after re-reveal: ToProviderDefs = %v, want %v", got, want)
	}
}

// Revealing, resetting and listing may run concurrently (race detector).
func TestDiscovery_RevealResetConcurrent(t *testing.T) {
	r := NewToolRegistry()
	for i := range 20 {
		r.RegisterHidden(&mockTool{name: fmt.Sprintf("alice_%d", i)})
	}
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := range 1000 {
			r.revealTool(fmt.Sprintf("alice_%d", i%20))
		}
	})
	wg.Go(func() {
		for range 1000 {
			r.ResetRevealed()
		}
	})
	wg.Go(func() {
		for range 1000 {
			r.ToProviderDefs()
		}
	})
	wg.Wait()
}

// Refreshing a server's tools (an MCP tools-changed notification or a
// reconnect) keeps a revealed tool revealed when it is still offered; the
// server's other tools stay hidden and a new tool arrives hidden.
func TestDiscovery_ReplaceByPrefixKeepsRevealed(t *testing.T) {
	r := NewToolRegistry()
	r.Register(&mockTool{name: "alice_core"})
	r.RegisterHidden(&mockTool{name: "mcp_bob_search"})
	r.RegisterHidden(&mockTool{name: "mcp_bob_fetch"})
	reveal(t, NewToolDetailsTool(r), "mcp_bob_search")

	r.ReplaceByPrefix("mcp_bob_", []ToolEntry{
		{Tool: &mockTool{name: "mcp_bob_search"}},
		{Tool: &mockTool{name: "mcp_bob_fetch"}},
		{Tool: &mockTool{name: "mcp_bob_lookup"}},
	})

	want := []string{"alice_core", "mcp_bob_search"}
	if got := providerDefNames(r); !slices.Equal(got, want) {
		t.Fatalf("after replace: ToProviderDefs = %v, want %v", got, want)
	}

	// A tool the refresh drops is gone; offered again later it arrives hidden.
	r.ReplaceByPrefix("mcp_bob_", nil)
	r.ReplaceByPrefix("mcp_bob_", []ToolEntry{{Tool: &mockTool{name: "mcp_bob_search"}}})
	want = []string{"alice_core"}
	if got := providerDefNames(r); !slices.Equal(got, want) {
		t.Fatalf("after drop and re-add: ToProviderDefs = %v, want %v", got, want)
	}
}

// Calling a registered but unrevealed tool tells the model to reveal it; an
// unknown name is still "not found"; the host path ignores discovery.
func TestDiscovery_HiddenToolRefusal(t *testing.T) {
	r, _ := discoveryRegistry(t)
	tests := []struct {
		name string
		tool string
		want string
	}{
		{"hidden", "alice_hidden", `tool "alice_hidden" is hidden; call get_tool_details("alice_hidden") first`},
		{"unknown", "bob_unknown", `tool "bob_unknown" not found`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := r.Execute(context.Background(), tc.tool, nil)
			if !res.IsError || res.ForLLM != tc.want {
				t.Fatalf("Execute(%q) = %q (error %v), want %q", tc.tool, res.ForLLM, res.IsError, tc.want)
			}
		})
	}
	if res := r.ExecuteForHost(context.Background(), "alice_hidden", nil, "", "", nil); res.IsError {
		t.Fatalf("host path refused a hidden tool: %s", res.ForLLM)
	}
}
