// ClawEh
// License: MIT

package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/PivotLLM/ctxengine"
	"github.com/PivotLLM/ctxengine/memory"
	"github.com/PivotLLM/ctxengine/session"

	"github.com/PivotLLM/ClawEh/agentreg"
	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/commands"
	"github.com/PivotLLM/ClawEh/providers"
	"github.com/PivotLLM/ClawEh/tools"
)

const discoveryTestSession = "agent:main:main"

// discoverySummary is a summarizer answer the compaction accepts.
const discoverySummary = `{"version":2,"state":{"goals":[{"text":"weather reports for Alice","refs":[{"seq_start":1,"seq_end":2}]}]},"covered_seq_start":0,"covered_seq_end":0}`

// discoveryAgent returns a test loop's default agent with a hidden tool,
// mock_custom, after seeding the session with seed exchanges (so a
// compaction has something to compress) and then revealing the tool through
// get_tool_details. A summarizer is in place for the compaction paths.
func discoveryAgent(t *testing.T, seed int) (*AgentLoop, *AgentInstance) {
	t.Helper()
	al := newTestAgentLoop(t).al
	agent := al.registry.Default()
	if agent == nil {
		t.Fatal("no default agent")
	}
	agent.Provider = &scriptedProvider{content: discoverySummary}
	agent.Tools.RegisterHidden(&mockCustomTool{})
	seedConversation(t, al, agent, seed)
	res := tools.NewToolDetailsTool(agent.Tools).Execute(context.Background(), map[string]any{"name": "mock_custom"})
	if res.IsError {
		t.Fatalf("get_tool_details: %s", res.ForLLM)
	}
	if !toolAdvertised(agent) {
		t.Fatal("precondition: the revealed tool is not in the tool list")
	}
	return al, agent
}

// toolAdvertised reports whether the revealed test tool is in the agent's
// tool list.
func toolAdvertised(agent *AgentInstance) bool {
	return slices.ContainsFunc(agent.Tools.ToProviderDefs(), func(d providers.ToolDefinition) bool {
		return d.Function.Name == "mock_custom"
	})
}

// seedConversation writes n long user/assistant exchanges through the
// session's context manager.
func seedConversation(t *testing.T, al *AgentLoop, agent *AgentInstance, n int) {
	t.Helper()
	cm, release := al.getContextManager(agent, discoveryTestSession)
	defer release()
	ctx := context.Background()
	filler := strings.Repeat(" The forecast stays mild and dry.", 60)
	for i := range n {
		if _, err := cm.AddUserMessage(ctx, providers.Message{Role: "user", Content: fmt.Sprintf("Alice asks about the weather in city %d.%s", i, filler)}); err != nil {
			t.Fatalf("AddUserMessage: %v", err)
		}
		if _, err := cm.AddAssistantMessage(ctx, providers.Message{Role: "assistant", Content: fmt.Sprintf("Bob reports sunshine in city %d.%s", i, filler)}); err != nil {
			t.Fatalf("AddAssistantMessage: %v", err)
		}
	}
}

// Clearing a session (/clear, session_clear, a single-shot turn's fresh start)
// and compacting its history (/compact, session_compact) hide the tools
// discovery revealed.
func TestDiscovery_ClearAndCompactHideRevealedTools(t *testing.T) {
	msg := bus.InboundMessage{Channel: "cli", ChatID: "direct", SessionKey: discoveryTestSession}
	tests := []struct {
		name string
		seed int
		run  func(t *testing.T, al *AgentLoop, agent *AgentInstance)
	}{
		{"/clear", 1, func(t *testing.T, al *AgentLoop, agent *AgentInstance) {
			t.Helper()
			if err := al.clearHistory(agent, &processOptions{SessionKey: discoveryTestSession}, msg); err != nil {
				t.Fatalf("clearHistory: %v", err)
			}
		}},
		{"session_clear", 1, func(t *testing.T, al *AgentLoop, agent *AgentInstance) {
			t.Helper()
			cm, release := al.getContextManager(agent, discoveryTestSession)
			defer release()
			opts := processOptions{SessionKey: discoveryTestSession, ResetSession: true}
			if err := al.prepareSession(context.Background(), agent, cm, &opts); err != nil {
				t.Fatalf("prepareSession: %v", err)
			}
		}},
		{"single-shot turn", 1, func(t *testing.T, al *AgentLoop, agent *AgentInstance) {
			t.Helper()
			agent.Spec.Fresh = true
			agent.Spec.Mode = agentreg.ModeSingleShot
			cm, release := al.getContextManager(agent, discoveryTestSession)
			defer release()
			opts := processOptions{SessionKey: discoveryTestSession}
			if err := al.prepareSession(context.Background(), agent, cm, &opts); err != nil {
				t.Fatalf("prepareSession: %v", err)
			}
		}},
		{"/compact", 30, func(t *testing.T, al *AgentLoop, agent *AgentInstance) {
			t.Helper()
			rt := &commands.Runtime{}
			al.addHistoryHooks(rt, agent, &processOptions{SessionKey: discoveryTestSession}, msg)
			if _, err := rt.CompactHistory(context.Background()); err != nil {
				t.Fatalf("CompactHistory: %v", err)
			}
		}},
		{"session_compact", 30, func(t *testing.T, al *AgentLoop, agent *AgentInstance) {
			t.Helper()
			if _, _, err := al.compactFn(agent)(context.Background(), discoveryTestSession); err != nil {
				t.Fatalf("session_compact: %v", err)
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			al, agent := discoveryAgent(t, tc.seed)
			tc.run(t, al, agent)
			if toolAdvertised(agent) {
				t.Fatal("the revealed tool is still in the tool list")
			}
		})
	}
}

// A manual compaction with nothing to compress leaves the history, and so the
// revealed tools, as they were.
func TestDiscovery_NothingToCompactKeepsRevealedTools(t *testing.T) {
	al, agent := discoveryAgent(t, 1)
	_, _, err := al.compactFn(agent)(context.Background(), discoveryTestSession)
	if !errors.Is(err, ctxengine.ErrNothingToCompress) {
		t.Fatalf("session_compact error = %v, want nothing to compress", err)
	}
	if !toolAdvertised(agent) {
		t.Fatal("a compaction that compacted nothing hid the revealed tool")
	}
}

// An automatic compaction (here the age trigger, firing inside
// AddUserMessage) hides the revealed tools through the engine's compaction
// reporter.
func TestDiscovery_AutomaticCompactionHidesRevealedTools(t *testing.T) {
	al, agent := discoveryAgent(t, 0)
	cm, release := al.getContextManager(agent, discoveryTestSession)
	defer release()
	ctx := context.Background()

	old := time.Now().AddDate(0, 0, -10)
	seeded := make([]memory.StoredMessage, 0, 6)
	for i := range 3 {
		for _, m := range []providers.Message{
			{Role: "user", Content: fmt.Sprintf("Alice asks about the weather in city %d.", i)},
			{Role: "assistant", Content: fmt.Sprintf("Bob reports sunshine in city %d.", i)},
		} {
			add := cm.AddAssistantMessage
			if m.Role == "user" {
				add = cm.AddUserMessage
			}
			seq, err := add(ctx, m)
			if err != nil {
				t.Fatalf("seed: %v", err)
			}
			seeded = append(seeded, memory.StoredMessage{Seq: seq, CreatedAt: old, Message: m})
		}
	}
	store, ok := agent.Sessions.(*session.SQLiteStore)
	if !ok {
		t.Fatalf("agent session store is %T, want *session.SQLiteStore", agent.Sessions)
	}
	if err := store.SetHistoryWithSeqs(discoveryTestSession, seeded); err != nil {
		t.Fatalf("backdate session: %v", err)
	}
	if !toolAdvertised(agent) {
		t.Fatal("precondition: compaction ran while seeding")
	}

	if _, err := cm.AddUserMessage(ctx, providers.Message{Role: "user", Content: "Alice asks again."}); err != nil {
		t.Fatalf("AddUserMessage: %v", err)
	}
	if agent.Sessions.GetSummary(discoveryTestSession) == "" {
		t.Fatal("the age-triggered compaction did not run")
	}
	if toolAdvertised(agent) {
		t.Fatal("the revealed tool is still in the tool list after an automatic compaction")
	}
}

// The context-window retry force-compresses the session; that compaction
// hides the revealed tools too (ForceCompress does not call the reporter).
func TestDiscovery_ForceCompressionHidesRevealedTools(t *testing.T) {
	const sessionKey = "force-compress"
	al, agent := discoveryAgent(t, 0)
	// A small window puts the history over the safety threshold, which is
	// what makes ForceCompress compact. Set before the session's context
	// manager is built.
	agent.ContextWindow = 20000
	store, ok := agent.Sessions.(*session.SQLiteStore)
	if !ok {
		t.Fatalf("agent session store is %T, want *session.SQLiteStore", agent.Sessions)
	}
	filler := strings.Repeat(" The forecast stays mild and dry.", 80)
	seeded := make([]memory.StoredMessage, 0, 24)
	for i := range 12 {
		seeded = append(seeded,
			memory.StoredMessage{Seq: int64(2*i + 1), Role: "user", Content: fmt.Sprintf("Alice asks about city %d.%s", i, filler)},
			memory.StoredMessage{Seq: int64(2*i + 2), Role: "assistant", Content: fmt.Sprintf("Bob answers for city %d.%s", i, filler)},
		)
	}
	if err := store.SetHistoryWithSeqs(sessionKey, seeded); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	turn := &llmTurn{al: al, agent: agent, opts: processOptions{SessionKey: sessionKey, Channel: "cli", ChatID: "direct"}}
	defer func() {
		for _, release := range turn.releases {
			release()
		}
	}()
	// The failed dispatch was built while the tool was revealed.
	d := &llmDispatch{defs: turn.toolDefinitions(), llmOpts: map[string]any{}}
	if !defsContain(d.defs, "mock_custom") {
		t.Fatal("precondition: the dispatch does not list the revealed tool")
	}
	turn.compressForRetry(context.Background(), d, errors.New("context_length_exceeded"), 1)

	if agent.Sessions.GetSummary(sessionKey) == "" {
		t.Fatal("the forced compaction did not run")
	}
	if toolAdvertised(agent) {
		t.Fatal("the revealed tool is still in the tool list after a forced compaction")
	}
	if defsContain(d.defs, "mock_custom") {
		t.Fatal("the retried request still advertises the tool the compaction hid")
	}
	if len(d.defs) == 0 {
		t.Fatal("the retried request lost the core tools")
	}
}

func defsContain(defs []providers.ToolDefinition, name string) bool {
	return slices.ContainsFunc(defs, func(d providers.ToolDefinition) bool { return d.Function.Name == name })
}
