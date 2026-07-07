// Tests for Name(), Description(), Parameters() of various tools — simple but needed for coverage.
package tools_test

import (
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/tools"
	toolsmsg "github.com/PivotLLM/ClawEh/pkg/tools/msg"
	toolsshell "github.com/PivotLLM/ClawEh/pkg/tools/shell"
)

func TestMessageTool_Metadata(t *testing.T) {
	tool := toolsmsg.NewMessageTool()
	if tool.Name() != "msg_send" {
		t.Errorf("Name() = %q, want msg_send", tool.Name())
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
	tool := toolsmsg.NewMessageTool()
	tool.ResetSentInRound()
	if tool.HasSentInRound() {
		t.Error("HasSentInRound() should be false after Reset")
	}
}

func TestMessageTool_HasSentInRound_Initial(t *testing.T) {
	tool := toolsmsg.NewMessageTool()
	if tool.HasSentInRound() {
		t.Error("HasSentInRound() should be false initially")
	}
}

func TestSearchTool_Metadata(t *testing.T) {
	registry := tools.NewToolRegistry()
	tool := tools.NewSearchTool(registry, 10)
	if tool.Name() != "search_tools" {
		t.Errorf("Name() = %q, want search_tools", tool.Name())
	}
	if tool.Description() == "" {
		t.Error("Description() should not be empty")
	}
	if tool.Parameters() == nil {
		t.Error("Parameters() should not be nil")
	}
}

func TestToolDetailsTool_Metadata(t *testing.T) {
	registry := tools.NewToolRegistry()
	tool := tools.NewToolDetailsTool(registry, 5)
	if tool.Name() != "get_tool_details" {
		t.Errorf("Name() = %q, want get_tool_details", tool.Name())
	}
	if tool.Description() == "" {
		t.Error("Description() should not be empty")
	}
	if tool.Parameters() == nil {
		t.Error("Parameters() should not be nil")
	}
}

func TestSendFileTool_Metadata(t *testing.T) {
	tool := toolsmsg.NewSendFileTool(t.TempDir(), false, 0, nil)
	if tool.Name() != "msg_send_file" {
		t.Errorf("Name() = %q, want msg_send_file", tool.Name())
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
	tool, err := toolsshell.NewExecToolWithConfig(t.TempDir(), true, cfg)
	if err != nil {
		t.Fatalf("NewExecToolWithConfig() error = %v", err)
	}
	if tool.Name() != "shell_exec" {
		t.Errorf("Name() = %q, want shell_exec", tool.Name())
	}
	if tool.Description() == "" {
		t.Error("Description() should not be empty")
	}
	params := tool.Parameters()
	if params == nil {
		t.Error("Parameters() should not be nil")
	}
}
