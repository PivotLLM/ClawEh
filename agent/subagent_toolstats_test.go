// ClawEh
// License: MIT

package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/providers"
	"github.com/PivotLLM/ClawEh/tools"
)

// TestRunSubagentTask_CarriesToolTally: a sub-agent run reports how many tool
// calls its worker made, how many failed and the last failure, and removes
// its tally entry when it ends.
func TestRunSubagentTask_CarriesToolTally(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()

	al := newTestAgentLoop(t).al
	agentInstance := al.registry.GetDefaultAgent()
	if agentInstance == nil {
		t.Fatal("no default agent")
	}
	agentInstance.Tools.Register(&noopWriteFile{})
	agentInstance.Tools.Register(&panickingTool{})
	if agentInstance.Config != nil {
		agentInstance.Config.Tools = []string{"*"}
	}
	agentInstance.Provider = &sequenceProvider{
		responses: []*providers.LLMResponse{
			{ToolCalls: []providers.ToolCall{toolCallTo("tc-1", "file_write")}},
			{ToolCalls: []providers.ToolCall{toolCallTo("tc-2", "no_such_tool")}},
			{ToolCalls: []providers.ToolCall{toolCallTo("tc-3", "panic_tool")}},
			{Content: "done"},
		},
		errors: []error{nil, nil, nil, nil},
	}

	session := "agent:" + agentInstance.ID + ":subagent:tally"
	res, err := al.runSubagentTask(context.Background(), agentInstance.ID, session, "do the work", "", nil)
	if err != nil {
		t.Fatalf("runSubagentTask: %v", err)
	}
	if res.ToolCalls != 3 || res.ToolErrors != 2 {
		t.Errorf("tally = %d calls, %d errors; want 3 and 2", res.ToolCalls, res.ToolErrors)
	}
	if !strings.HasPrefix(res.LastToolError, "panic_tool: ") {
		t.Errorf("last tool error = %q, want the panic_tool failure", res.LastToolError)
	}
	if res.SessionKey != session {
		t.Errorf("session key = %q, want %q", res.SessionKey, session)
	}
	if calls, _, _ := tools.EndToolStats(session); calls != 0 {
		t.Errorf("tally entry left behind after the run (%d calls)", calls)
	}
}

// TestRunSubagentTask_ErrorPathRemovesTally: a sub-agent run that fails still
// removes its tally entry, so later calls under that key are not counted.
func TestRunSubagentTask_ErrorPathRemovesTally(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()

	al := newTestAgentLoop(t).al
	agentInstance := al.registry.GetDefaultAgent()
	if agentInstance == nil {
		t.Fatal("no default agent")
	}
	agentInstance.Tools.Register(&noopWriteFile{})
	if agentInstance.Config != nil {
		agentInstance.Config.Tools = []string{"*"}
	}
	boom := errors.New("provider exploded")
	// One working tool call, then the provider fails on every attempt.
	responses := make([]*providers.LLMResponse, 11)
	responses[0] = &providers.LLMResponse{ToolCalls: []providers.ToolCall{toolCallTo("tc-1", "file_write")}}
	errs := make([]error, 11)
	for i := 1; i < len(errs); i++ {
		errs[i] = boom
	}
	agentInstance.Provider = &sequenceProvider{responses: responses, errors: errs}

	session := "agent:" + agentInstance.ID + ":subagent:tally-error"
	if _, err := al.runSubagentTask(context.Background(), agentInstance.ID, session, "do the work", "", nil); err == nil {
		t.Fatal("runSubagentTask succeeded; want the provider error")
	}
	tools.RecordToolResult(session, "file_write", tools.NewToolResult("late"))
	if calls, _, _ := tools.EndToolStats(session); calls != 0 {
		t.Errorf("tally entry left behind after a failed run (%d calls)", calls)
	}
}

// toolCallTo is a provider tool call to name with no arguments.
func toolCallTo(id, name string) providers.ToolCall {
	return providers.ToolCall{
		ID: id, Type: "function", Name: name,
		Function: &providers.FunctionCall{Name: name, Arguments: "{}"},
	}
}

// panickingTool panics when executed.
type panickingTool struct{}

func (panickingTool) Name() string        { return "panic_tool" }
func (panickingTool) Description() string { return "panics" }
func (panickingTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}

func (panickingTool) Execute(context.Context, map[string]any) *tools.ToolResult {
	panic("tool blew up")
}
