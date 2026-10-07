package tools

import (
	"slices"
	"testing"
)

// TestRemoveByPrefix pins the primitive the MCP re-registration path relies on:
// every entry with the prefix goes, visible or hidden, the version moves so
// cached definitions rebuild, and unrelated tools are untouched.
func TestRemoveByPrefix(t *testing.T) {
	r := NewToolRegistry()
	r.Register(&mockRegistryTool{name: "mcp_alice_search"})
	r.RegisterHidden(&mockRegistryTool{name: "mcp_alice_fetch"})
	r.Register(&mockRegistryTool{name: "mcp_bob_search"})
	r.Register(&mockRegistryTool{name: "file_read"})

	before := r.Version()
	if got := r.RemoveByPrefix("mcp_alice_"); got != 2 {
		t.Fatalf("RemoveByPrefix returned %d, want 2", got)
	}
	if r.Version() == before {
		t.Fatal("RemoveByPrefix must bump the registry version")
	}

	names := r.List()
	for _, gone := range []string{"mcp_alice_search", "mcp_alice_fetch"} {
		if slices.Contains(names, gone) {
			t.Errorf("%s should have been removed; got %v", gone, names)
		}
	}
	for _, kept := range []string{"mcp_bob_search", "file_read"} {
		if !slices.Contains(names, kept) {
			t.Errorf("%s should have been kept; got %v", kept, names)
		}
	}

	// Nothing left with the prefix: no removal, no version bump.
	after := r.Version()
	if got := r.RemoveByPrefix("mcp_alice_"); got != 0 {
		t.Fatalf("second RemoveByPrefix returned %d, want 0", got)
	}
	if r.Version() != after {
		t.Fatal("a removal that removed nothing must not bump the version")
	}
}

// TestReplaceByPrefix: the prefix's old tools are swapped for the new entries
// (visible and hidden kept as given), unrelated tools stay, and the version
// moves so cached definitions rebuild.
func TestReplaceByPrefix(t *testing.T) {
	r := NewToolRegistry()
	r.Register(&mockRegistryTool{name: "mcp_alice_search"})
	r.RegisterHidden(&mockRegistryTool{name: "mcp_alice_fetch"})
	r.Register(&mockRegistryTool{name: "mcp_bob_search"})

	before := r.Version()
	removed := r.ReplaceByPrefix("mcp_alice_", []ToolEntry{
		{Tool: &mockRegistryTool{name: "mcp_alice_search"}, IsCore: true},
		{Tool: &mockRegistryTool{name: "mcp_alice_lookup"}, Group: "alice", RevealTogether: true},
	})
	if removed != 2 {
		t.Fatalf("ReplaceByPrefix removed %d, want 2", removed)
	}
	if r.Version() == before {
		t.Fatal("ReplaceByPrefix must bump the registry version")
	}
	want := []string{"mcp_alice_lookup", "mcp_alice_search", "mcp_bob_search"}
	if got := r.List(); !slices.Equal(got, want) {
		t.Fatalf("tools = %v, want %v", got, want)
	}
	if _, ok := r.Get("mcp_alice_search"); !ok {
		t.Error("mcp_alice_search should be visible")
	}
	if _, ok := r.Get("mcp_alice_lookup"); ok {
		t.Error("mcp_alice_lookup was registered hidden and should not be visible")
	}

	// Nothing under the prefix and nothing new: no change, no version bump.
	r.ReplaceByPrefix("mcp_carol_", nil)
	after := r.Version()
	if r.ReplaceByPrefix("mcp_carol_", nil) != 0 || r.Version() != after {
		t.Fatal("an empty replacement must remove nothing and leave the version alone")
	}
}
