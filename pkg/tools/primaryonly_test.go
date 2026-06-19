package tools

import (
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
