package tools

import (
	"context"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/global"
)

func TestIsPrimaryOnly_GlobalFlagPropagates(t *testing.T) {
	primary := wrapGlobalTool("ns", global.ToolDefinition{Name: "x", PrimaryOnly: true})
	if !IsPrimaryOnly(primary) {
		t.Error("PrimaryOnly def should wrap to a primary-only tool")
	}
	normal := wrapGlobalTool("ns", global.ToolDefinition{Name: "y"})
	if IsPrimaryOnly(normal) {
		t.Error("unflagged def should not be primary-only")
	}
	// Propagates through the session/async variants too.
	sess := wrapGlobalTool("ns", global.ToolDefinition{Name: "z", PrimaryOnly: true, SessionScoped: true})
	if !IsPrimaryOnly(sess) {
		t.Error("PrimaryOnly should propagate through the session wrapper")
	}
}

// fakeTool is a minimal Tool for registry tests; primaryOnly toggles the
// optional PrimaryOnlyTool interface.
type fakeTool struct {
	name        string
	primaryOnly bool
}

func (f fakeTool) Name() string                                        { return f.name }
func (f fakeTool) Description() string                                 { return f.name }
func (f fakeTool) Parameters() map[string]any                          { return map[string]any{"type": "object"} }
func (f fakeTool) Execute(context.Context, map[string]any) *ToolResult { return &ToolResult{} }
func (f fakeTool) IsPrimaryOnly() bool                                 { return f.primaryOnly }

func TestRegistry_PrimaryOnlyFiltering(t *testing.T) {
	r := NewToolRegistry()
	r.Register(fakeTool{name: "read"})
	r.Register(fakeTool{name: "spawn", primaryOnly: true})

	if !r.IsPrimaryOnlyTool("spawn") {
		t.Error("spawn should be primary-only")
	}
	if r.IsPrimaryOnlyTool("read") {
		t.Error("read should not be primary-only")
	}
	if r.IsPrimaryOnlyTool("nope") {
		t.Error("unknown tool should report false")
	}

	defs := r.ToProviderDefsExcludingPrimaryOnly()
	for _, d := range defs {
		if d.Function.Name == "spawn" {
			t.Fatalf("primary-only tool should be excluded from sub-agent defs: %+v", defs)
		}
	}
	// The non-primary tool survives.
	found := false
	for _, d := range defs {
		if d.Function.Name == "read" {
			found = true
		}
	}
	if !found {
		t.Errorf("non-primary tool should remain in sub-agent defs: %+v", defs)
	}
}
