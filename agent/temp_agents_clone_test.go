// ClawEh
// License: MIT

package agent

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/agentreg"
	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/routing"
)

// cloneFixture is a loop whose one agent, main, has every native tool, the
// Maestro and cogmem suites, an external MCP server's tools, a mount, a file
// in files/, its own AGENTS.md text and a memory; with a recording model.
type cloneFixture struct {
	al    *AgentLoop
	cfg   *config.Config
	model *recordingProvider
	main  *AgentInstance
}

const (
	sourceAgentsText = "Alice's own instructions: answer in haiku."
	sourceMemory     = "Alice prefers coffee."
)

func newCloneFixture(t *testing.T) *cloneFixture {
	t.Helper()
	_, ts := newRefreshTestServer(t)
	mountDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(mountDir, "mounted.txt"), []byte("from the mount\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := toolRegTestConfig(t)
	cfg.Agents.Defaults.RestrictToWorkspace = true // file tools resolve paths in the workspace
	a := &cfg.Agents.List[0]
	a.MCPTools = []string{"svc"}
	a.Maestro = &config.MaestroConfig{Enabled: true}
	a.Mounts = []config.MountConfig{{Name: "ref", Path: mountDir}}
	cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{
		"svc": {Enabled: true, Type: "http", URL: ts.URL},
	}

	model := &recordingProvider{}
	al := mustNewAgentLoop(t, cfg, bus.NewMessageBus(), model, nil)
	t.Cleanup(func() { al.Close(context.Background()) })
	if err := al.EnsureMCPInitialized(context.Background()); err != nil {
		t.Fatalf("EnsureMCPInitialized: %v", err)
	}
	main, _ := al.GetRegistry().Get("main")
	if err := os.WriteFile(filepath.Join(main.Workspace, "AGENTS.md"), []byte(sourceAgentsText+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(main.Workspace, "files", "notes.txt"), []byte("from files\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if res := runTool(t, main, "cogmem_memory_create", map[string]any{"type": "fact", "text": sourceMemory}); res.IsError {
		t.Fatalf("source memory_create: %s", res.ForLLM)
	}
	return &cloneFixture{al: al, cfg: cfg, model: model, main: main}
}

func sortedTools(a *AgentInstance) []string {
	names := a.Tools.List()
	slices.Sort(names)
	return names
}

// mainTurn runs one turn of agent on a subagent channel and returns the
// session key it ran in.
func mainTurn(t *testing.T, al *AgentLoop, agent *AgentInstance, message string) string {
	t.Helper()
	key := routing.BuildAgentMainSessionKey(agent.ID)
	if _, err := al.runAgentLoop(context.Background(), agent, processOptions{
		SessionKey: key, Channel: "subagent", ChatID: key, UserMessage: message,
	}); err != nil {
		t.Fatalf("turn on %s: %v", agent.Label(), err)
	}
	return key
}

func historyContains(agent *AgentInstance, key, text string) bool {
	for _, m := range agent.Sessions.GetHistory(key) {
		if strings.Contains(m.Content, text) {
			return true
		}
	}
	return false
}

// TestClone_IsACopyOfItsSource: a clone has its source's tools (native, MCP
// and suites), models, prompt files, files and mounts, and memories, and
// offers the model the same tools and prompt.
func TestClone_IsACopyOfItsSource(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()
	f := newCloneFixture(t)
	reg := f.al.GetRegistry()

	id, err := reg.Create(config.AgentConfig{}, agentreg.CloneOf("main"))
	if err != nil {
		t.Fatalf("Create clone: %v", err)
	}
	clone, _ := reg.Get(id)

	want := sortedTools(f.main)
	for _, name := range []string{"mcp_svc_ping", "cogmem_memory_search", "file_read_lines"} {
		if !slices.Contains(want, name) {
			t.Fatalf("fixture: the source lacks %s (has %v)", name, want)
		}
	}
	if !slices.ContainsFunc(want, func(n string) bool { return strings.HasPrefix(n, "maestro_") }) {
		t.Fatalf("fixture: the source has no Maestro tools (%v)", want)
	}
	if got := sortedTools(clone); !slices.Equal(got, want) {
		t.Fatalf("clone tools differ from the source's:\nclone  %v\nsource %v", got, want)
	}

	if !reflect.DeepEqual(clone.Candidates, f.main.Candidates) || !slices.Equal(clone.Config.Models, f.main.Config.Models) {
		t.Fatalf("clone models %v / %v, source %v / %v", clone.Config.Models, clone.Candidates, f.main.Config.Models, f.main.Candidates)
	}
	if got, want := clone.ContextBuilder.BuildSystemPrompt(), f.main.ContextBuilder.BuildSystemPrompt(); got != want {
		t.Fatalf("clone static prompt differs from the source's:\nclone  %q\nsource %q", got, want)
	}

	for path, text := range map[string]string{"files/notes.txt": "from files", "ref/mounted.txt": "from the mount"} {
		res := runTool(t, clone, "file_read_lines", map[string]any{"path": path})
		if res.IsError || !strings.Contains(res.ForLLM, text) {
			t.Fatalf("clone reading %s: %s", path, res.ForLLM)
		}
	}

	mainTurn(t, f.al, clone, "hello")
	call := f.model.last(t)
	sys := call.systemPrompt(t)
	if !strings.Contains(sys, sourceAgentsText) || !strings.Contains(sys, sourceMemory) {
		t.Fatalf("clone system prompt lacks the source's AGENTS.md text or memory:\n%s", sys)
	}
	offered := slices.Clone(call.tools)
	mainTurn(t, f.al, f.main, "hello")
	sourceOffered := f.model.last(t).tools
	slices.Sort(offered)
	sourceOffered = slices.Clone(sourceOffered)
	slices.Sort(sourceOffered)
	if !slices.Equal(offered, sourceOffered) {
		t.Fatalf("the model was offered different tools:\nclone  %v\nsource %v", offered, sourceOffered)
	}
}

// TestClone_OwnMemoryAndConversation: what a clone writes to memory or says
// stays with it, and what the source does after the clone was made never
// reaches the clone. Deleting the clone removes its state, never the shared
// workspace.
func TestClone_OwnMemoryAndConversation(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()
	f := newCloneFixture(t)
	reg := f.al.GetRegistry()

	id, err := reg.Create(config.AgentConfig{}, agentreg.CloneOf("main"))
	if err != nil {
		t.Fatalf("Create clone: %v", err)
	}
	clone, _ := reg.Get(id)

	if res := runTool(t, clone, "cogmem_memory_create", map[string]any{"type": "fact", "text": "Bob prefers tea."}); res.IsError {
		t.Fatalf("clone memory_create: %s", res.ForLLM)
	}
	if res := runTool(t, f.main, "cogmem_memory_create", map[string]any{"type": "fact", "text": "Carrots are orange."}); res.IsError {
		t.Fatalf("source memory_create: %s", res.ForLLM)
	}
	noMatch := func(a *AgentInstance, q string) bool {
		res := runTool(t, a, "cogmem_memory_search", map[string]any{"query": q})
		return !res.IsError && strings.Contains(res.ForLLM, "No active memories match")
	}
	if !noMatch(f.main, "Bob prefers tea.") {
		t.Fatal("a clone's memory reached the source")
	}
	if !noMatch(clone, "Carrots are orange.") {
		t.Fatal("the source's later memory reached the clone")
	}
	if noMatch(clone, sourceMemory) {
		t.Fatal("the clone lost the source's memory from before it was made")
	}

	cloneKey := mainTurn(t, f.al, clone, "said to the clone")
	mainKey := mainTurn(t, f.al, f.main, "said to the source")
	if historyContains(f.main, mainKey, "said to the clone") {
		t.Fatal("the clone's conversation reached the source")
	}
	if historyContains(clone, cloneKey, "said to the source") {
		t.Fatal("the source's conversation reached the clone")
	}

	if err := reg.Delete(id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(clone.StateDir); !os.IsNotExist(err) {
		t.Fatalf("clone state dir left behind: %v", err)
	}
	for _, p := range []string{"AGENTS.md", "files/notes.txt"} {
		if _, err := os.Stat(filepath.Join(f.main.Workspace, p)); err != nil {
			t.Fatalf("deleting the clone removed the source's %s: %v", p, err)
		}
	}
	if !historyContains(f.main, mainKey, "said to the source") {
		t.Fatal("deleting the clone touched the source's conversation")
	}
}

// TestClone_FollowsSourceConfigOnReload: after a reload that narrows the
// source's tools, the clone has exactly the source's new tool set.
func TestClone_FollowsSourceConfigOnReload(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()
	f := newCloneFixture(t)

	id, err := f.al.GetRegistry().Create(config.AgentConfig{}, agentreg.CloneOf("main"))
	if err != nil {
		t.Fatalf("Create clone: %v", err)
	}

	next := *f.cfg
	next.Agents.List = slices.Clone(f.cfg.Agents.List)
	next.Agents.List[0].Tools = []string{"file_read_lines"}
	next.Agents.List[0].Maestro = nil
	if err := f.al.ReloadProviderAndConfig(context.Background(), f.model, &next); err != nil {
		t.Fatalf("reload: %v", err)
	}
	main, _ := f.al.GetRegistry().Get("main")
	clone, ok := f.al.GetRegistry().Get(id)
	if !ok {
		t.Fatal("the clone did not survive the reload")
	}
	want := sortedTools(main)
	if slices.Contains(want, "file_write") || slices.ContainsFunc(want, func(n string) bool { return strings.HasPrefix(n, "maestro_") }) {
		t.Fatalf("fixture: the reload did not narrow the source's tools: %v", want)
	}
	if got := sortedTools(clone); !slices.Equal(got, want) {
		t.Fatalf("after reload clone tools %v, source %v", got, want)
	}
}
