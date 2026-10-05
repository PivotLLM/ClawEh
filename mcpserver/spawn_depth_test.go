// ClawEh
// License: MIT

package mcpserver

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/tools"
	toolsagents "github.com/PivotLLM/ClawEh/tools/agents"
)

// depthTool records the sub-agent depth it ran at. When async is set it also
// completes through the callback, so the re-injected turn's depth can be read.
type depthTool struct {
	name  string
	async bool
	got   int
}

func (d *depthTool) Name() string               { return d.name }
func (d *depthTool) Description() string        { return "records spawn depth" }
func (d *depthTool) Parameters() map[string]any { return map[string]any{} }
func (d *depthTool) Execute(ctx context.Context, _ map[string]any) *tools.ToolResult {
	d.got = toolsagents.SpawnDepth(ctx)
	return &tools.ToolResult{ForLLM: "ok"}
}

func (d *depthTool) ExecuteAsync(ctx context.Context, _ map[string]any, cb tools.AsyncCallback) *tools.ToolResult {
	d.got = toolsagents.SpawnDepth(ctx)
	if d.async && cb != nil {
		cb(ctx, &tools.ToolResult{ForLLM: "late result"})
	}
	return &tools.ToolResult{ForLLM: "started", Async: true}
}

// spawnTool is agent_spawn over the real Spawner, so its depth guard runs.
type spawnTool struct{ sp *toolsagents.Spawner }

func (s *spawnTool) Name() string               { return "agent_spawn" }
func (s *spawnTool) Description() string        { return "spawn" }
func (s *spawnTool) Parameters() map[string]any { return map[string]any{} }
func (s *spawnTool) Execute(ctx context.Context, _ map[string]any) *tools.ToolResult {
	res, err := s.sp.Spawn(ctx, global.SpawnRequest{Mode: global.SpawnAndWait, Task: "work"})
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	return &tools.ToolResult{ForLLM: res.ForLLM, IsError: res.IsError}
}

func dispatchDepth(t *testing.T, st *SessionTokenStore, tok string, tool *depthTool) int {
	t.Helper()
	regs := map[string]*tools.ToolRegistry{"alice": newRegistryWith(tool)}
	out, isErr := dispatchToolCall(context.Background(), tool.name,
		map[string]any{"session_token": tok}, st, resolverFor(regs), nil, nil, nil, nil)
	if isErr {
		t.Fatalf("dispatch failed: %s", out)
	}
	return tool.got
}

// A tool call presented with a session token runs at the depth the loop
// recorded for that session's current turn; a later turn's depth replaces it.
func TestDispatch_RunsAtSessionTurnDepth(t *testing.T) {
	st := newSessionTokenStore()
	tok := st.Issue("alice", "agent:alice:main", "/ws/alice/sessions")

	for _, tc := range []struct {
		name  string
		depth int
	}{
		{"before any turn", -1},
		{"sub-agent turn", 2},
		{"following ordinary turn", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := 0
			if tc.depth >= 0 {
				st.SetDepth("agent:alice:main", tc.depth)
				want = tc.depth
			}
			if got := dispatchDepth(t, st, tok, &depthTool{name: "probe"}); got != want {
				t.Errorf("tool ran at depth %d, want %d", got, want)
			}
		})
	}
}

// The depth survives a token rotation (session reset, eviction reissue).
func TestSessionTokenStore_DepthSurvivesReissue(t *testing.T) {
	st := newSessionTokenStore()
	st.Issue("alice", "agent:alice:main", "/ws")
	st.SetDepth("agent:alice:main", 2)
	tok := st.Issue("alice", "agent:alice:main", "/ws")
	if got := dispatchDepth(t, st, tok, &depthTool{name: "probe"}); got != 2 {
		t.Errorf("depth after reissue = %d, want 2", got)
	}
}

// A service token has no turn: its calls run at depth 0 even while the
// agent's conversation token carries a depth.
func TestDispatch_ServiceTokenStaysAtDepthZero(t *testing.T) {
	st := newSessionTokenStore()
	st.Issue("alice", "agent:alice:main", "/ws")
	svc := "SST" + strings.Repeat("ab", 32)
	st.RegisterService(svc, "alice", "/ws")
	st.SetDepth("agent:alice:main", 2)
	if got := dispatchDepth(t, st, svc, &depthTool{name: "probe"}); got != 0 {
		t.Errorf("service token call ran at depth %d, want 0", got)
	}
}

// agent_spawn over MCP is refused once the session's turn is at the limit,
// and allowed below it.
func TestDispatch_AgentSpawnRefusedAtDepthLimit(t *testing.T) {
	mgr := toolsagents.NewSubagentManager(toolsagents.SubagentManagerConfig{
		Workspace: t.TempDir(),
		Live:      toolsagents.NewLiveSet(),
		RunFull: func(_ context.Context, _, task, _ string, _ []string) (*global.SyncResult, func(), error) {
			return &global.SyncResult{Content: task, Iterations: 1}, func() {}, nil
		},
	})
	sp := toolsagents.NewSpawner(mgr)
	sp.SetMaxDepth(2)
	regs := map[string]*tools.ToolRegistry{"alice": newRegistryWith(&spawnTool{sp: sp})}

	st := newSessionTokenStore()
	tok := st.Issue("alice", "agent:alice:main", "/ws")

	for _, tc := range []struct {
		depth   int
		refused bool
	}{
		{1, false},
		{2, true},
	} {
		st.SetDepth("agent:alice:main", tc.depth)
		out, isErr := dispatchToolCall(context.Background(), "agent_spawn",
			map[string]any{"session_token": tok}, st, resolverFor(regs), nil, nil, nil, nil)
		refused := isErr && strings.Contains(out, "maximum sub-agent depth")
		if refused != tc.refused {
			t.Errorf("depth %d: refused=%v, want %v (out=%q)", tc.depth, refused, tc.refused, out)
		}
	}
}

// An async tool's late result re-enters the session at the turn's depth.
func TestDispatch_AsyncReinjectCarriesTurnDepth(t *testing.T) {
	st := newSessionTokenStore()
	tok := st.Issue("alice", "agent:alice:main", "/ws")
	st.SetSource("agent:alice:main", "telegram", "42")
	st.SetDepth("agent:alice:main", 2)

	msgBus := bus.NewMessageBus()
	defer msgBus.Close()
	got := make(chan bus.InboundMessage, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if m, ok := msgBus.ConsumeInbound(ctx); ok {
			got <- m
		}
	}()

	tool := &depthTool{name: "probe", async: true}
	regs := map[string]*tools.ToolRegistry{"alice": newRegistryWith(tool)}
	if out, isErr := dispatchToolCall(context.Background(), "probe",
		map[string]any{"session_token": tok}, st, resolverFor(regs), nil, nil, msgBus, nil); isErr {
		t.Fatalf("dispatch failed: %s", out)
	}
	select {
	case m := <-got:
		if m.Metadata[bus.MetaSpawnDepth] != "2" {
			t.Errorf("re-injected spawn_depth = %q, want 2", m.Metadata[bus.MetaSpawnDepth])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no re-injected message")
	}
}
