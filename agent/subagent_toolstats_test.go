// ClawEh
// License: MIT

package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/providers"
	"github.com/PivotLLM/ClawEh/routing"
	"github.com/PivotLLM/ClawEh/tools"
)

// agentRecordingProvider wraps a provider and records the agent each call was
// made for, so a test can find the temporary clone a sub-agent ran as.
type agentRecordingProvider struct {
	providers.LLMProvider
	mu     sync.Mutex
	agents []string
}

func (p *agentRecordingProvider) Chat(
	ctx context.Context, messages []providers.Message, defs []providers.ToolDefinition, model string, opts map[string]any,
) (*providers.LLMResponse, error) {
	p.mu.Lock()
	p.agents = append(p.agents, providers.AgentIDFromContext(ctx))
	p.mu.Unlock()
	return p.LLMProvider.Chat(ctx, messages, defs, model, opts)
}

func (p *agentRecordingProvider) lastAgent() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.agents) == 0 {
		return ""
	}
	return p.agents[len(p.agents)-1]
}

// newSubagentTestLoop builds a loop whose one agent ("main") allows every
// tool and whose model calls go to provider. Tools added with RegisterTool
// reach the sub-agent clones built later.
func newSubagentTestLoop(t *testing.T, provider providers.LLMProvider) *AgentLoop {
	t.Helper()
	cfg := newTestConfig(t)
	cfg.Agents.List[0].Tools = []string{"*"}
	return mustNewAgentLoop(t, cfg, bus.NewMessageBus(), provider, nil)
}

// TestRunSubagentTask_CarriesToolTally: a sub-agent run reports how many tool
// calls its worker made, how many failed and the last failure, and removes
// its tally entry when it ends.
func TestRunSubagentTask_CarriesToolTally(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()

	provider := &sequenceProvider{
		responses: []*providers.LLMResponse{
			{ToolCalls: []providers.ToolCall{toolCallTo("tc-1", "file_write")}},
			{ToolCalls: []providers.ToolCall{toolCallTo("tc-2", "no_such_tool")}},
			{ToolCalls: []providers.ToolCall{toolCallTo("tc-3", "panic_tool")}},
			{Content: "done"},
		},
		errors: []error{nil, nil, nil, nil},
	}
	al := newSubagentTestLoop(t, provider)
	al.RegisterTool(&noopWriteFile{})
	al.RegisterTool(&panickingTool{})

	res, release, err := al.runSubagentTask(context.Background(), "main", "do the work", "", nil)
	defer release()
	if err != nil {
		t.Fatalf("runSubagentTask: %v", err)
	}
	if res.ToolCalls != 3 || res.ToolErrors != 2 {
		t.Errorf("tally = %d calls, %d errors; want 3 and 2", res.ToolCalls, res.ToolErrors)
	}
	if !strings.HasPrefix(res.LastToolError, "panic_tool: ") {
		t.Errorf("last tool error = %q, want the panic_tool failure", res.LastToolError)
	}
	pk := routing.ParseAgentSessionKey(res.SessionKey)
	if pk == nil || pk.Rest != routing.DefaultMainKey || !al.GetRegistry().IsTemp(pk.AgentID) {
		t.Errorf("session key = %q, want the main session of a temporary clone", res.SessionKey)
	}
	if calls, _, _ := tools.EndToolStats(res.SessionKey); calls != 0 {
		t.Errorf("tally entry left behind after the run (%d calls)", calls)
	}
}

// TestRunSubagentTask_ErrorPathRemovesTally: a sub-agent run that fails still
// removes its tally entry, so later calls under that key are not counted, and
// its clone is deleted once released.
func TestRunSubagentTask_ErrorPathRemovesTally(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()

	boom := errors.New("provider exploded")
	// One working tool call, then the provider fails on every attempt.
	responses := make([]*providers.LLMResponse, 11)
	responses[0] = &providers.LLMResponse{ToolCalls: []providers.ToolCall{toolCallTo("tc-1", "file_write")}}
	errs := make([]error, 11)
	for i := 1; i < len(errs); i++ {
		errs[i] = boom
	}
	provider := &agentRecordingProvider{LLMProvider: &sequenceProvider{responses: responses, errors: errs}}
	al := newSubagentTestLoop(t, provider)
	al.RegisterTool(&noopWriteFile{})

	_, release, err := al.runSubagentTask(context.Background(), "main", "do the work", "", nil)
	if err == nil {
		t.Fatal("runSubagentTask succeeded; want the provider error")
	}
	release()
	clone := provider.lastAgent()
	if clone == "" || clone == "main" {
		t.Fatalf("the run was not made by a clone (agent %q)", clone)
	}
	session := routing.BuildAgentMainSessionKey(clone)
	tools.RecordToolResult(session, "file_write", tools.NewToolResult("late"))
	if calls, _, _ := tools.EndToolStats(session); calls != 0 {
		t.Errorf("tally entry left behind after a failed run (%d calls)", calls)
	}
	if temps := al.GetRegistry().ListTemp(); len(temps) != 0 {
		t.Errorf("clone left in the registry after release: %v", temps)
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
