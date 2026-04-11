package tools

import (
	"context"
	"testing"
)

func TestToolRegistry_GetDefinitions_Empty(t *testing.T) {
	r := NewToolRegistry()
	defs := r.GetDefinitions()
	if len(defs) != 0 {
		t.Errorf("GetDefinitions() len = %d, want 0", len(defs))
	}
}

func TestToolRegistry_GetDefinitions_WithTools(t *testing.T) {
	r := NewToolRegistry()
	r.Register(&mockTool{name: "tool_a"})
	r.Register(&mockTool{name: "tool_b"})

	defs := r.GetDefinitions()
	if len(defs) != 2 {
		t.Errorf("GetDefinitions() len = %d, want 2", len(defs))
	}
}

func TestToolRegistry_GetSummaries_WithHidden(t *testing.T) {
	r := NewToolRegistry()
	r.Register(&mockTool{name: "alpha"})
	r.Register(&mockTool{name: "beta"})
	r.RegisterHidden(&mockTool{name: "hidden"}) // TTL=0, should not appear

	summaries := r.GetSummaries()
	if len(summaries) != 2 {
		t.Errorf("GetSummaries() len = %d, want 2 (hidden not shown when TTL=0)", len(summaries))
	}
}

func TestToolRegistry_ToProviderDefs_WithTools(t *testing.T) {
	r := NewToolRegistry()
	r.Register(&mockTool{name: "tool_x"})

	defs := r.ToProviderDefs()
	if len(defs) != 1 {
		t.Fatalf("ToProviderDefs() len = %d, want 1", len(defs))
	}
	if defs[0].Function.Name != "tool_x" {
		t.Errorf("Function.Name = %q, want tool_x", defs[0].Function.Name)
	}
}

func TestToolRegistry_ListSorted(t *testing.T) {
	r := NewToolRegistry()
	r.Register(&mockTool{name: "z_tool"})
	r.Register(&mockTool{name: "a_tool"})

	list := r.List()
	if len(list) != 2 {
		t.Fatalf("List() len = %d, want 2", len(list))
	}
	// should be sorted
	if list[0] != "a_tool" || list[1] != "z_tool" {
		t.Errorf("List() = %v, want [a_tool z_tool]", list)
	}
}

func TestToolRegistry_CountWithRegistrations(t *testing.T) {
	r := NewToolRegistry()
	if r.Count() != 0 {
		t.Errorf("Count() = %d, want 0", r.Count())
	}
	r.Register(&mockTool{name: "t1_unique"})
	r.Register(&mockTool{name: "t2_unique"})
	if r.Count() != 2 {
		t.Errorf("Count() = %d, want 2", r.Count())
	}
}

func TestToolRegistry_HiddenTool_NotCallableAfterTTLExpired(t *testing.T) {
	r := NewToolRegistry()
	r.RegisterHidden(&mockTool{name: "hidden"})

	// TTL starts at 0 — hidden tool should not be callable.
	_, ok := r.Get("hidden")
	if ok {
		t.Error("hidden tool with TTL=0 should not be callable")
	}
}

func TestToolRegistry_HiddenTool_CallableAfterPromotion(t *testing.T) {
	r := NewToolRegistry()
	r.RegisterHidden(&mockTool{name: "promoted"})
	r.PromoteTools([]string{"promoted"}, 2)

	tool, ok := r.Get("promoted")
	if !ok {
		t.Fatal("promoted tool should be callable")
	}
	if tool.Name() != "promoted" {
		t.Errorf("Name() = %q, want promoted", tool.Name())
	}
}

func TestToolRegistry_TickTTL_DecrementsHiddenTools(t *testing.T) {
	r := NewToolRegistry()
	r.RegisterHidden(&mockTool{name: "ticking"})
	r.PromoteTools([]string{"ticking"}, 1)

	// After 1 tick, TTL should reach 0.
	r.TickTTL()

	_, ok := r.Get("ticking")
	if ok {
		t.Error("tool should not be callable after TTL expires")
	}
}

func TestToolRegistry_SnapshotHiddenTools(t *testing.T) {
	r := NewToolRegistry()
	r.RegisterHidden(&mockTool{name: "snap1"})
	r.RegisterHidden(&mockTool{name: "snap2"})
	r.Register(&mockTool{name: "core"})

	snap := r.SnapshotHiddenTools()
	if len(snap.Docs) != 2 {
		t.Errorf("SnapshotHiddenTools() len = %d, want 2 (hidden only)", len(snap.Docs))
	}
	if snap.Version == 0 {
		t.Error("Version should be non-zero after registrations")
	}
}

func TestToolRegistry_Execute_NotFoundExtra(t *testing.T) {
	r := NewToolRegistry()
	result := r.Execute(context.Background(), "absolutely_nonexistent_xyz", nil)
	if !result.IsError {
		t.Error("Execute() of nonexistent tool should return error")
	}
}

func TestToolRegistry_Execute_FoundWithResult(t *testing.T) {
	r := NewToolRegistry()
	r.Register(&mockTool{name: "do_thing_unique", result: NewToolResult("done")})

	result := r.Execute(context.Background(), "do_thing_unique", nil)
	if result.IsError {
		t.Fatalf("Execute() error: %s", result.ForLLM)
	}
	if result.ForLLM != "done" {
		t.Errorf("result = %q, want done", result.ForLLM)
	}
}

func TestToolRegistry_Version_IncreasesOnRegister(t *testing.T) {
	r := NewToolRegistry()
	v0 := r.Version()
	r.Register(&mockTool{name: "v1"})
	v1 := r.Version()
	if v1 <= v0 {
		t.Errorf("Version should increase after Register: v0=%d, v1=%d", v0, v1)
	}
}
