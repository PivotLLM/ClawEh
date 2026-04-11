// Tests for Name(), Description(), Parameters() of various tools — simple but needed for coverage.
package tools

import (
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/config"
)

func TestMessageTool_Metadata(t *testing.T) {
	tool := NewMessageTool()
	if tool.Name() != "message" {
		t.Errorf("Name() = %q, want message", tool.Name())
	}
	if tool.Description() == "" {
		t.Error("Description() should not be empty")
	}
	params := tool.Parameters()
	if params == nil {
		t.Error("Parameters() should not be nil")
	}
}

func TestMessageTool_ResetSentInRound(t *testing.T) {
	tool := NewMessageTool()
	tool.ResetSentInRound()
	if tool.HasSentInRound() {
		t.Error("HasSentInRound() should be false after Reset")
	}
}

func TestMessageTool_HasSentInRound_Initial(t *testing.T) {
	tool := NewMessageTool()
	if tool.HasSentInRound() {
		t.Error("HasSentInRound() should be false initially")
	}
}

func TestRegexSearchTool_Metadata(t *testing.T) {
	registry := NewToolRegistry()
	tool := NewRegexSearchTool(registry, 3, 10)
	if tool.Name() != "tool_search_tool_regex" {
		t.Errorf("Name() = %q, want tool_search_tool_regex", tool.Name())
	}
	if tool.Description() == "" {
		t.Error("Description() should not be empty")
	}
	params := tool.Parameters()
	if params == nil {
		t.Error("Parameters() should not be nil")
	}
}

func TestBM25SearchTool_Metadata(t *testing.T) {
	registry := NewToolRegistry()
	tool := NewBM25SearchTool(registry, 3, 10)
	if tool.Name() != "tool_search_tool_bm25" {
		t.Errorf("Name() = %q, want tool_search_tool_bm25", tool.Name())
	}
	if tool.Description() == "" {
		t.Error("Description() should not be empty")
	}
	params := tool.Parameters()
	if params == nil {
		t.Error("Parameters() should not be nil")
	}
}

func TestSendFileTool_Metadata(t *testing.T) {
	tool := NewSendFileTool(t.TempDir(), false, 0, nil)
	if tool.Name() != "send_file" {
		t.Errorf("Name() = %q, want send_file", tool.Name())
	}
	if tool.Description() == "" {
		t.Error("Description() should not be empty")
	}
	params := tool.Parameters()
	if params == nil {
		t.Error("Parameters() should not be nil")
	}
}

func TestExecTool_Metadata(t *testing.T) {
	cfg := config.DefaultConfig()
	tool, err := NewExecToolWithConfig(t.TempDir(), true, cfg)
	if err != nil {
		t.Fatalf("NewExecToolWithConfig() error = %v", err)
	}
	if tool.Name() != "exec" {
		t.Errorf("Name() = %q, want exec", tool.Name())
	}
	if tool.Description() == "" {
		t.Error("Description() should not be empty")
	}
	params := tool.Parameters()
	if params == nil {
		t.Error("Parameters() should not be nil")
	}
}
