// ClawEh
// License: MIT

package agent

import (
	"context"
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
	if agentInstance.Config != nil {
		agentInstance.Config.Tools = []string{"*"}
	}
	call := func(id, name string) providers.ToolCall {
		return providers.ToolCall{
			ID: id, Type: "function", Name: name,
			Function: &providers.FunctionCall{Name: name, Arguments: "{}"},
		}
	}
	agentInstance.Provider = &sequenceProvider{
		responses: []*providers.LLMResponse{
			{ToolCalls: []providers.ToolCall{call("tc-1", "file_write")}},
			{ToolCalls: []providers.ToolCall{call("tc-2", "no_such_tool")}},
			{Content: "done"},
		},
		errors: []error{nil, nil, nil},
	}

	session := "agent:" + agentInstance.ID + ":subagent:tally"
	res, err := al.runSubagentTask(context.Background(), agentInstance.ID, session, "do the work", "", nil)
	if err != nil {
		t.Fatalf("runSubagentTask: %v", err)
	}
	if res.ToolCalls != 2 || res.ToolErrors != 1 {
		t.Errorf("tally = %d calls, %d errors; want 2 and 1", res.ToolCalls, res.ToolErrors)
	}
	if !strings.HasPrefix(res.LastToolError, "no_such_tool: ") {
		t.Errorf("last tool error = %q, want the no_such_tool failure", res.LastToolError)
	}
	if calls, _, _ := tools.EndToolStats(session); calls != 0 {
		t.Errorf("tally entry left behind after the run (%d calls)", calls)
	}
}
