// ClawEh
// License: MIT

package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/PivotLLM/ctxengine"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/providers"
	"github.com/PivotLLM/ClawEh/tools"
)

// notebookTool is a hidden tool the discovery tests find and call.
type notebookTool struct {
	calls atomic.Int32
}

func (n *notebookTool) Name() string        { return "mcp_alice_notebook" }
func (n *notebookTool) Description() string { return "Read a page of Alice's notebook" }
func (n *notebookTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}

func (n *notebookTool) Execute(context.Context, map[string]any) *tools.ToolResult {
	n.calls.Add(1)
	return tools.SilentResult("page 1: buy milk")
}

// defsRecordingProvider answers from a script and records the tool definitions
// and messages of every request.
type defsRecordingProvider struct {
	mu        sync.Mutex
	responses []*providers.LLMResponse
	defs      [][]providers.ToolDefinition
	messages  [][]providers.Message
}

func (p *defsRecordingProvider) Chat(_ context.Context, messages []providers.Message, defs []providers.ToolDefinition, _ string, _ map[string]any) (*providers.LLMResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.defs = append(p.defs, slices.Clone(defs))
	p.messages = append(p.messages, slices.Clone(messages))
	i := len(p.defs) - 1
	if i >= len(p.responses) {
		return &providers.LLMResponse{Content: "done"}, nil
	}
	return p.responses[i], nil
}

func (p *defsRecordingProvider) GetDefaultModel() string { return "test" }

func toolCallResponse(id, name, args string) *providers.LLMResponse {
	return &providers.LLMResponse{ToolCalls: []providers.ToolCall{{
		ID: id, Type: "function", Name: name,
		Function: &providers.FunctionCall{Name: name, Arguments: args},
	}}}
}

// toolMessageFor returns the tool message answering callID in msgs.
func toolMessageFor(msgs []providers.Message, callID string) (providers.Message, bool) {
	for _, m := range msgs {
		if m.Role == "tool" && m.ToolCallID == callID {
			return m, true
		}
	}
	return providers.Message{}, false
}

// A turn in which the model searches, then calls the hidden tool it found:
// every request offers the same tool definitions (the hidden tool deferred,
// search_tools as the tool-search tool), the search result carries the hit as
// a tool reference into the next request and into the stored session, and
// the hidden tool runs.
func TestDiscovery_TurnSendsSameToolsAndRunsFoundTool(t *testing.T) {
	al := newTestAgentLoop(t).al
	agent := al.registry.Default()
	if agent == nil {
		t.Fatal("no default agent")
	}
	notebook := &notebookTool{}
	agent.Tools.RegisterSuiteHidden(notebook)
	agent.Tools.RegisterSuite(tools.NewSearchTool(agent.Tools, 5))
	agent.Tools.RegisterSuite(tools.NewToolDetailsTool(agent.Tools))

	provider := &defsRecordingProvider{responses: []*providers.LLMResponse{
		toolCallResponse("tc-search", "search_tools", `{"query":"Alice notebook page"}`),
		toolCallResponse("tc-read", "mcp_alice_notebook", `{}`),
		{Content: "Alice's notebook says to buy milk."},
	}}
	agent.Provider = provider

	const key = "agent:main:discovery-defer"
	opts := processOptions{
		SessionKey:      key,
		Channel:         "cli",
		ChatID:          "direct",
		UserMessage:     "What is on Alice's notebook page?",
		DefaultResponse: defaultResponse,
	}
	got, err := al.runAgentLoop(context.Background(), agent, opts)
	if err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	if got != "Alice's notebook says to buy milk." {
		t.Errorf("answer = %q", got)
	}
	if n := notebook.calls.Load(); n != 1 {
		t.Errorf("hidden tool ran %d times, want 1", n)
	}

	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.defs) != 3 {
		t.Fatalf("provider called %d times, want 3", len(provider.defs))
	}
	for i := 1; i < len(provider.defs); i++ {
		if !reflect.DeepEqual(provider.defs[i], provider.defs[0]) {
			t.Fatalf("request %d offered different tools than request 0:\n got %+v\nwant %+v", i, provider.defs[i], provider.defs[0])
		}
	}
	var sawNotebook, sawSearch bool
	for _, d := range provider.defs[0] {
		switch d.Function.Name {
		case "mcp_alice_notebook":
			sawNotebook = true
			if !d.Deferred {
				t.Error("the hidden tool is not offered deferred")
			}
		case "search_tools":
			sawSearch = true
			if !d.ToolSearch || d.Deferred {
				t.Errorf("search_tools: ToolSearch=%v Deferred=%v", d.ToolSearch, d.Deferred)
			}
		}
	}
	if !sawNotebook || !sawSearch {
		t.Fatalf("tool list lacks the hidden tool (%v) or search_tools (%v)", sawNotebook, sawSearch)
	}

	want := []string{"mcp_alice_notebook"}
	msg, ok := toolMessageFor(provider.messages[1], "tc-search")
	if !ok {
		t.Fatal("second request lacks the search result")
	}
	if !slices.Equal(msg.ToolReferences, want) {
		t.Errorf("search result references in the request = %v, want %v", msg.ToolReferences, want)
	}

	stored, ok := toolMessageFor(agent.Sessions.GetHistory(key), "tc-search")
	if !ok {
		t.Fatal("stored session lacks the search result")
	}
	if !slices.Equal(stored.ToolReferences, want) {
		t.Errorf("stored search result references = %v, want %v", stored.ToolReferences, want)
	}
	if read, ok := toolMessageFor(agent.Sessions.GetHistory(key), "tc-read"); !ok || len(read.ToolReferences) != 0 {
		t.Errorf("stored hidden-tool result = %+v (found %v), want no references", read, ok)
	}
}

// toolResultMessage copies a result's tool references onto the tool message,
// and leaves them empty when the result has none.
func TestToolResultMessage_ToolReferences(t *testing.T) {
	al := newTestAgentLoop(t).al
	turn := &llmTurn{al: al, agent: al.registry.Default()}
	tc := providers.ToolCall{ID: "tc-1", Name: "search_tools"}

	res := tools.SilentResult("Found 2 tool(s)")
	res.ToolReferences = []string{"mcp_alice_read", "mcp_bob_fetch"}
	msg := turn.toolResultMessage(toolCallResult{tc: tc, result: res})
	if !slices.Equal(msg.ToolReferences, res.ToolReferences) {
		t.Errorf("ToolReferences = %v, want %v", msg.ToolReferences, res.ToolReferences)
	}
	if msg.Role != "tool" || msg.ToolCallID != "tc-1" || msg.Content != "Found 2 tool(s)" {
		t.Errorf("message = %+v", msg)
	}

	plain := turn.toolResultMessage(toolCallResult{tc: tc, result: tools.SilentResult("ok")})
	if plain.ToolReferences != nil {
		t.Errorf("ToolReferences = %v, want none", plain.ToolReferences)
	}
}

// The session store keeps a tool message's references across a close and
// reopen.
func TestSessionStore_KeepsToolReferences(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	modes := config.Modes{Dir: 0o700, File: 0o600}
	const key = "agent:main:refs"

	store, err := initSessionStore(dir, modes)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []providers.Message{
		{Role: "user", Content: "find Alice's notebook"},
		{Role: "assistant", ToolCalls: []providers.ToolCall{{
			ID: "tc-1", Type: "function", Name: "search_tools",
			Function: &providers.FunctionCall{Name: "search_tools", Arguments: `{"query":"notebook"}`},
		}}},
		{Role: "tool", ToolCallID: "tc-1", Content: "Found 1 tool(s)", ToolReferences: []string{"mcp_alice_notebook"}},
	} {
		if _, addErr := store.AddFullMessage(key, m); addErr != nil {
			t.Fatalf("AddFullMessage: %v", addErr)
		}
	}
	closeT(t, store)

	reopened, err := initSessionStore(dir, modes)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeT(t, reopened) })
	msg, ok := toolMessageFor(reopened.GetHistory(key), "tc-1")
	if !ok {
		t.Fatal("tool message not stored")
	}
	if want := []string{"mcp_alice_notebook"}; !slices.Equal(msg.ToolReferences, want) {
		t.Errorf("ToolReferences after reopen = %v, want %v", msg.ToolReferences, want)
	}
}

// bulkyTool is a hidden tool with a long description, so leaving it out of
// an estimate is visible.
type bulkyTool struct{ name string }

func (b bulkyTool) Name() string { return b.name }
func (b bulkyTool) Description() string {
	return "Bob's upstream tool. " + strings.Repeat("It does many things. ", 40)
}

func (b bulkyTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}

func (b bulkyTool) Execute(context.Context, map[string]any) *tools.ToolResult {
	return tools.SilentResult("ok")
}

// The tool-schema cost on the context manager's request counts only what the
// request sends: with discovery on and no references, the hidden tools cost
// nothing; once a stored message references one, that one tool is added. The
// in-turn path (the messages in hand) uses the same selection.
func TestAssembleRequest_EstimatesOnlySentTools(t *testing.T) {
	al := newTestAgentLoop(t).al
	agent := al.registry.Default()
	if agent == nil {
		t.Fatal("no default agent")
	}
	agent.Tools.RegisterSuite(tools.NewSearchTool(agent.Tools, 5))
	agent.Tools.RegisterSuite(tools.NewToolDetailsTool(agent.Tools))
	coreDefs := agent.Tools.ToProviderDefs()

	for i := range 200 {
		agent.Tools.RegisterHidden(bulkyTool{name: fmt.Sprintf("mcp_bob_tool_%03d", i)})
	}
	// search_tools' description lists the hidden namespaces, so the core
	// cost is taken again with the hidden tools registered.
	var coreNow []providers.ToolDefinition
	var referenced providers.ToolDefinition
	for _, d := range agent.Tools.ToProviderDefs() {
		if !d.Deferred {
			coreNow = append(coreNow, d)
		}
		if d.Function.Name == "mcp_bob_tool_007" {
			referenced = d
		}
	}
	coreTokens := ctxengine.EstimateToolDefinitionTokens(coreNow)
	referenced.Deferred = false
	wantOne := ctxengine.EstimateToolDefinitionTokens(append(slices.Clone(coreNow), referenced))
	if wantOne <= coreTokens {
		t.Fatalf("precondition: one tool must cost something (%d <= %d)", wantOne, coreTokens)
	}
	if len(coreNow) != len(coreDefs) {
		t.Fatalf("core tools = %d, want %d", len(coreNow), len(coreDefs))
	}

	const key = "agent:main:estimate"
	opts := processOptions{SessionKey: key, Channel: "cli", ChatID: "direct"}
	ctx := context.Background()

	if got := al.assembleRequest(ctx, agent, nil, opts).ToolDefinitionTokens; got != coreTokens {
		t.Errorf("no references: tool tokens = %d, want the core tools' %d", got, coreTokens)
	}

	history := []providers.Message{
		{Role: "user", Content: "find Bob's tool"},
		{Role: "assistant", ToolCalls: []providers.ToolCall{{
			ID: "tc-1", Type: "function", Name: "search_tools",
			Function: &providers.FunctionCall{Name: "search_tools", Arguments: `{"query":"bob"}`},
		}}},
		{Role: "tool", ToolCallID: "tc-1", Content: "Found 1 tool(s)", ToolReferences: []string{"mcp_bob_tool_007"}},
	}
	cm, release := al.getContextManager(agent, key)
	defer release()
	if _, err := cm.AddUserMessage(ctx, history[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := cm.AddToolCallMessage(ctx, history[1]); err != nil {
		t.Fatal(err)
	}
	if _, err := cm.AddToolResult(ctx, history[2]); err != nil {
		t.Fatal(err)
	}

	if got := al.assembleRequest(ctx, agent, nil, opts).ToolDefinitionTokens; got != wantOne {
		t.Errorf("one stored reference: tool tokens = %d, want %d (core plus that tool)", got, wantOne)
	}
	defs := agent.Tools.ToProviderDefs()
	if got := al.assembleRequestWithDefs(ctx, agent, nil, opts, defs, history).ToolDefinitionTokens; got != wantOne {
		t.Errorf("in-turn messages with one reference: tool tokens = %d, want %d", got, wantOne)
	}
	if got := al.assembleRequestWithDefs(ctx, agent, nil, opts, defs, history[:1]).ToolDefinitionTokens; got != coreTokens {
		t.Errorf("in-turn messages without references: tool tokens = %d, want %d", got, coreTokens)
	}
}
