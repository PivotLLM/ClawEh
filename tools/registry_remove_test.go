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
