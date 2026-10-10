// ClawEh
// License: MIT

package agent

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/agentreg"
	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/providers"
	"github.com/PivotLLM/ClawEh/routing"
	"github.com/PivotLLM/ClawEh/tools"
	toolsagents "github.com/PivotLLM/ClawEh/tools/agents"
)

// runTool runs one of agent's tools in its main session and fails the test
// when the tool is missing.
func runTool(t *testing.T, agent *AgentInstance, name string, args map[string]any) *tools.ToolResult {
	t.Helper()
	tool, ok := agent.Tools.Get(name)
	if !ok {
		t.Fatalf("%s has no tool %s (has %v)", agent.Label(), name, agent.Tools.List())
	}
	ctx := tools.WithSessionKey(context.Background(), routing.BuildAgentMainSessionKey(agent.ID))
	return tool.Execute(ctx, args)
}

// tempRootEntries lists what is left under the temporary agents' directory.
func tempRootEntries(t *testing.T, reg *AgentRegistry) []string {
	t.Helper()
	entries, err := os.ReadDir(reg.TempRoot())
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// TestClone_SharesWorkspaceOwnsConversationAndMemory: a clone works in its
// source's workspace, starts with its source's memories, and keeps its own
// conversation and memory: what it writes never reaches the source.
func TestClone_SharesWorkspaceOwnsConversationAndMemory(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()

	tl := newTestAgentLoop(t)
	al := tl.al
	reg := al.GetRegistry()
	main, _ := reg.Get("main")

	if res := runTool(t, main, "cogmem_memory_create", map[string]any{"type": "fact", "text": "Alice prefers coffee."}); res.IsError {
		t.Fatalf("source memory_create: %s", res.ForLLM)
	}

	id, err := reg.CreateClone("main", agentreg.EphemeralMemory())
	if err != nil {
		t.Fatalf("Create clone: %v", err)
	}
	clone, _ := reg.Get(id)
	if clone.Workspace != main.Workspace {
		t.Fatalf("clone workspace = %q, want the source's %q", clone.Workspace, main.Workspace)
	}
	if clone.StateDir == main.StateDir {
		t.Fatal("a clone must have its own state directory")
	}

	// The source's memories are visible to the clone.
	if res := runTool(t, clone, "cogmem_memory_search", map[string]any{"query": "Alice prefers coffee."}); res.IsError ||
		strings.Contains(res.ForLLM, "No active memories match") {
		t.Fatalf("the clone should see the source's memory: %s", res.ForLLM)
	}
	// The clone's writes stay on its copy.
	if res := runTool(t, clone, "cogmem_memory_create", map[string]any{"type": "fact", "text": "Bob prefers tea."}); res.IsError {
		t.Fatalf("clone memory_create: %s", res.ForLLM)
	}
	if res := runTool(t, main, "cogmem_memory_search", map[string]any{"query": "Bob prefers tea."}); res.IsError || !strings.Contains(res.ForLLM, "No active memories match") {
		t.Fatalf("a clone's memory reached the source: %s", res.ForLLM)
	}

	// A turn on the clone is in the clone's conversation, not the source's.
	key := routing.BuildAgentMainSessionKey(clone.ID)
	if _, err := al.runAgentLoop(context.Background(), clone, processOptions{
		SessionKey: key, Channel: "subagent", ChatID: key, UserMessage: "hello from the clone",
	}); err != nil {
		t.Fatalf("clone turn: %v", err)
	}
	if h := clone.Sessions.GetHistory(key); len(h) == 0 {
		t.Fatal("the clone's conversation is empty after its turn")
	}
	for _, m := range main.Sessions.GetHistory(routing.BuildAgentMainSessionKey("main")) {
		if strings.Contains(m.Content, "hello from the clone") {
			t.Fatal("the clone's turn landed in the source's conversation")
		}
	}

	if err := reg.Delete(id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(clone.StateDir); !os.IsNotExist(err) {
		t.Fatalf("clone state dir left behind: %v", err)
	}
	if _, err := os.Stat(main.Workspace); err != nil {
		t.Fatalf("deleting a clone touched the source's workspace: %v", err)
	}
}

// TestFreshTempAgent_HasNoTools: a temporary agent that is not a clone gets no
// tools at all, whatever its configuration allows, including the host-built
// and cognitive-memory tools, and an empty workspace of its own.
func TestFreshTempAgent_HasNoTools(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()

	al := newTestAgentLoop(t).al
	al.RegisterTool(&noopWriteFile{})
	reg := al.GetRegistry()

	id, err := reg.CreateFresh(config.AgentConfig{Tools: []string{"*"}, MCPTools: []string{"*"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	fresh, _ := reg.Get(id)
	al.RegisterTool(&panickingTool{})
	if n := fresh.Tools.Count(); n != 0 {
		t.Fatalf("fresh temporary agent has %d tools (%v), want none", n, fresh.Tools.List())
	}
	if entries, readErr := os.ReadDir(fresh.Workspace); readErr != nil || len(entries) != 0 {
		t.Fatalf("fresh agent workspace must exist and be empty (no prompt files, no skills): %v %v", entries, readErr)
	}
	main, _ := reg.Get("main")
	if fresh.Workspace == main.Workspace || !strings.HasPrefix(fresh.Workspace, fresh.StateDir) {
		t.Fatalf("fresh workspace = %q, want its own under %q", fresh.Workspace, fresh.StateDir)
	}

	// A clone, by contrast, gets the source's tools and the host-built ones.
	cloneID, err := reg.CreateClone("main")
	if err != nil {
		t.Fatalf("Create clone: %v", err)
	}
	clone, _ := reg.Get(cloneID)
	if clone.Tools.Count() == 0 || clone.Tools.Count() != main.Tools.Count() {
		t.Fatalf("clone has %d tools, source %d; want the same toolset", clone.Tools.Count(), main.Tools.Count())
	}
}

// TestTempAgents_InvisibleToOperators: creating temporary agents changes
// nothing an operator or a router sees — the configuration (behind
// /api/agents, the Check Up report and the session list), the agent list,
// the default agent and binding routes.
func TestTempAgents_InvisibleToOperators(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()

	al := newTestAgentLoop(t).al
	reg := al.GetRegistry()
	cfg := al.GetConfig()
	agentsBefore, dirsBefore := len(cfg.Agents.List), cfg.AgentSessionDirs()

	if _, err := reg.CreateClone("main"); err != nil {
		t.Fatalf("Create clone: %v", err)
	}
	if _, err := reg.CreateFresh(config.AgentConfig{}); err != nil {
		t.Fatalf("Create fresh: %v", err)
	}
	if len(cfg.Agents.List) != agentsBefore || len(cfg.AgentSessionDirs()) != len(dirsBefore) {
		t.Fatal("creating temporary agents changed the configuration")
	}
	if got := reg.List(); len(got) != 1 || got[0] != "main" {
		t.Fatalf("List = %v, want only the config agent", got)
	}
	if reg.DefaultID() != "main" {
		t.Fatalf("default = %q, want main", reg.DefaultID())
	}
	info, ok := al.GetStartupInfo()["agents"].(map[string]any)
	if !ok {
		t.Fatal("startup info has no agents block")
	}
	if count, ok := info["count"].(int); !ok || count != 1 {
		t.Fatalf("startup info lists %v agents, want 1", info["count"])
	}
	for _, id := range reg.ListTemp() {
		route, agent, err := al.resolveMessageRoute(bus.InboundMessage{
			Channel: "telegram", Content: "hi", Metadata: map[string]string{"mentioned_agent": id},
		})
		if err != nil || agent.ID != "main" || route.AgentID == id {
			t.Fatalf("a message naming %s routed to %v (%v)", id, agent, err)
		}
	}
}

// TestSubagentSpawn_CreatesAndDeletesClone: agent_spawn (wait and background)
// and Maestro dispatch (Spawner.RunSync) each run the task as a clone of the
// target, and leave no temporary agent or directory behind.
func TestSubagentSpawn_CreatesAndDeletesClone(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()

	provider := &agentRecordingProvider{LLMProvider: &mockProvider{}}
	al := newSubagentTestLoop(t, provider)
	reg := al.GetRegistry()
	main, _ := reg.Get("main")

	assertCloneRanAndWentAway := func(t *testing.T, what string) {
		t.Helper()
		ran := provider.lastAgent()
		if ran == "" || ran == "main" {
			t.Fatalf("%s: the task did not run as a clone (agent %q)", what, ran)
		}
		if _, ok := reg.Get(ran); ok {
			t.Fatalf("%s: clone %s still registered", what, ran)
		}
		if left := tempRootEntries(t, reg); len(left) != 0 {
			t.Fatalf("%s: left behind under the temp root: %v", what, left)
		}
	}

	t.Run("agent_spawn wait", func(t *testing.T) {
		res, err := main.spawnMgr.Run(context.Background(), "do it", "job", "", "cli", "direct", "", nil)
		if err != nil || res.IsError {
			t.Fatalf("Run: %v %+v", err, res)
		}
		assertCloneRanAndWentAway(t, "wait")
	})

	t.Run("agent_spawn background", func(t *testing.T) {
		done := make(chan *tools.ToolResult, 1)
		if _, err := main.spawnMgr.SpawnCallback("do it", "job", "", "cli", "direct", "", nil,
			func(_ context.Context, r *tools.ToolResult) { done <- r }, 0); err != nil {
			t.Fatalf("SpawnCallback: %v", err)
		}
		if r := <-done; r.IsError {
			t.Fatalf("background task failed: %+v", r)
		}
		// The clone is deleted right after the callback returns.
		for range 100 {
			if len(reg.ListTemp()) == 0 && len(tempRootEntries(t, reg)) == 0 {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		assertCloneRanAndWentAway(t, "background")
	})

	t.Run("maestro dispatch", func(t *testing.T) {
		res, err := toolsagents.NewSpawner(main.spawnMgr).RunSync(context.Background(), "do it", "")
		if err != nil || res.Content != "Mock response" {
			t.Fatalf("RunSync: %v %+v", err, res)
		}
		assertCloneRanAndWentAway(t, "maestro")
	})
}

// TestSubagentSpawn_UnknownModelCreatesNoClone: a model the target does not
// have is refused before any clone is made.
func TestSubagentSpawn_UnknownModelCreatesNoClone(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()

	al := newTestAgentLoop(t).al
	_, release, err := al.runSubagentTask(context.Background(), "main", "do it", "no-such-model", nil)
	release()
	if !errors.Is(err, global.ErrModelNotAvailable) {
		t.Fatalf("err = %v, want ErrModelNotAvailable", err)
	}
	if temps := al.GetRegistry().ListTemp(); len(temps) != 0 {
		t.Fatalf("a refused spawn created clones: %v", temps)
	}
}

// TestReload_RebuildsTempAgents: a config reload keeps the temporary agents
// and rebuilds them, tools included, against the new configuration.
func TestReload_RebuildsTempAgents(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()

	tl := newTestAgentLoop(t)
	al := tl.al
	reg := al.GetRegistry()
	cloneID, err := reg.CreateClone("main")
	if err != nil {
		t.Fatalf("Create clone: %v", err)
	}
	freshID, err := reg.CreateFresh(config.AgentConfig{})
	if err != nil {
		t.Fatalf("Create fresh: %v", err)
	}
	before, _ := reg.Get(cloneID)

	if err := al.ReloadProviderAndConfig(context.Background(), tl.provider, tl.cfg); err != nil {
		t.Fatalf("reload: %v", err)
	}
	after, ok := reg.Get(cloneID)
	if !ok || after == before {
		t.Fatalf("clone kept=%v rebuilt=%v; want kept and rebuilt", ok, after != before)
	}
	if after.Tools.Count() == 0 {
		t.Fatal("the rebuilt clone has no tools")
	}
	if fresh, ok := reg.Get(freshID); !ok || fresh.Tools.Count() != 0 {
		t.Fatal("the fresh agent must survive the reload, still without tools")
	}
}

// modelRecordingProvider records the model of every call.
type modelRecordingProvider struct {
	providers.LLMProvider
	mu     sync.Mutex
	models []string
}

func (p *modelRecordingProvider) Chat(
	ctx context.Context, messages []providers.Message, defs []providers.ToolDefinition, model string, opts map[string]any,
) (*providers.LLMResponse, error) {
	p.mu.Lock()
	p.models = append(p.models, model)
	p.mu.Unlock()
	return p.LLMProvider.Chat(ctx, messages, defs, model, opts)
}

// TestSubagentSpawn_ModelNarrowsClone: a sub-agent asked for one of the
// target's models is a clone on that model alone, the way a forum
// participant is (agentreg.CloneModel), and runs on it.
func TestSubagentSpawn_ModelNarrowsClone(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()

	cfg := newTestConfig(t)
	cfg.Models = []config.ModelConfig{
		{ModelName: "one", Model: "one-wire", Provider: "p", Enabled: true},
		{ModelName: "two", Model: "two-wire", Provider: "p", Enabled: true},
	}
	cfg.Agents.List[0].Models = []string{"one", "two"}
	provider := &modelRecordingProvider{LLMProvider: &mockProvider{}}
	al := mustNewAgentLoop(t, cfg, bus.NewMessageBus(), provider, nil)

	var cloneModel string
	var candidates int
	provider.LLMProvider = &inspectingProvider{LLMProvider: &mockProvider{}, inspect: func(ctx context.Context) {
		id := providers.AgentIDFromContext(ctx)
		if info, ok := al.GetRegistry().Info(id); ok {
			cloneModel = info.Spec.CloneModel
		}
		if inst, ok := al.GetRegistry().Get(id); ok {
			candidates = len(inst.Candidates)
		}
	}}
	_, release, err := al.runSubagentTask(context.Background(), "main", "do it", "two", nil)
	release()
	if err != nil {
		t.Fatalf("runSubagentTask: %v", err)
	}
	if cloneModel != "two" || candidates != 1 {
		t.Fatalf("clone model = %q with %d candidates, want two alone", cloneModel, candidates)
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.models) == 0 || provider.models[0] != "two-wire" {
		t.Fatalf("models called = %v, want two-wire", provider.models)
	}
}

// inspectingProvider calls inspect with each call's context.
type inspectingProvider struct {
	providers.LLMProvider
	inspect func(context.Context)
}

func (p *inspectingProvider) Chat(
	ctx context.Context, messages []providers.Message, defs []providers.ToolDefinition, model string, opts map[string]any,
) (*providers.LLMResponse, error) {
	p.inspect(ctx)
	return p.LLMProvider.Chat(ctx, messages, defs, model, opts)
}
