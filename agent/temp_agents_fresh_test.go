// ClawEh
// License: MIT

package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PivotLLM/cogmem"
	cogmemstore "github.com/PivotLLM/cogmem/store"

	"github.com/PivotLLM/ClawEh/agentreg"
	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/cogmemhost"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/providers"
	"github.com/PivotLLM/ClawEh/routing"
	"github.com/PivotLLM/ClawEh/utils"
)

// recordedCall is what the model was sent on one call.
type recordedCall struct {
	messages []providers.Message
	tools    []string
}

// recordingProvider is a scripted model: it answers "reply N" to the Nth call
// and records the messages and the tool names of every call. With gate set,
// each call signals started and then waits for a release on gate (or for its
// context to end, which fails the call).
type recordingProvider struct {
	mu      sync.Mutex
	calls   []recordedCall
	gate    chan struct{}
	started chan struct{}
}

// newGatedRecorder is a recordingProvider whose calls wait for the test.
func newGatedRecorder() *recordingProvider {
	return &recordingProvider{gate: make(chan struct{}), started: make(chan struct{}, 32)}
}

func (p *recordingProvider) Chat(ctx context.Context, messages []providers.Message, defs []providers.ToolDefinition, _ string, _ map[string]any) (*providers.LLMResponse, error) {
	names := make([]string, 0, len(defs))
	for _, d := range defs {
		names = append(names, d.Function.Name)
	}
	p.mu.Lock()
	p.calls = append(p.calls, recordedCall{messages: slices.Clone(messages), tools: names})
	n := len(p.calls)
	p.mu.Unlock()
	if p.gate != nil {
		p.started <- struct{}{}
		select {
		case <-p.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return &providers.LLMResponse{Content: fmt.Sprintf("reply %d", n)}, nil
}

// count is how many calls the model received.
func (p *recordingProvider) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

// call returns the i-th call (0-based).
func (p *recordingProvider) call(i int) recordedCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls[i]
}

// waitStarted fails the test unless a gated call starts in time.
func (p *recordingProvider) waitStarted(t *testing.T) {
	t.Helper()
	select {
	case <-p.started:
	case <-time.After(5 * time.Second):
		t.Fatal("model call did not start")
	}
}

func (p *recordingProvider) GetDefaultModel() string { return "mock-model" }

// last returns the most recent call and fails the test when there is none.
func (p *recordingProvider) last(t *testing.T) recordedCall {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.calls) == 0 {
		t.Fatal("the model was never called")
	}
	return p.calls[len(p.calls)-1]
}

// systemPrompt is the call's system message; the test fails without one.
func (c recordedCall) systemPrompt(t *testing.T) string {
	t.Helper()
	if len(c.messages) == 0 || c.messages[0].Role != "system" {
		t.Fatalf("the request has no leading system message: %+v", c.messages)
	}
	return c.messages[0].Content
}

// conversation is the call's messages after the system message, as
// "role: content" lines.
func (c recordedCall) conversation() []string {
	var out []string
	for _, m := range c.messages {
		if m.Role != "system" {
			out = append(out, m.Role+": "+m.Content)
		}
	}
	return out
}

// freshLoop is an owner loop (it saves and restores temporary agents) with a
// recording model, on a data directory of its own.
func freshLoop(t *testing.T) (*AgentLoop, *recordingProvider, *config.Config) {
	t.Helper()
	cfg := ownerTestConfig(t)
	p := &recordingProvider{}
	return mustNewAgentLoop(t, cfg, bus.NewMessageBus(), p, nil, OwnsDataDir()), p, cfg
}

// turn runs one turn of agent id in its main conversation, on a user channel
// (so a channel-specific runtime block would show if one were added).
func turn(t *testing.T, al *AgentLoop, id, message string) {
	t.Helper()
	agent, ok := al.GetRegistry().Get(id)
	if !ok {
		t.Fatalf("agent %s not registered", id)
	}
	key := routing.BuildAgentMainSessionKey(agent.ID)
	if _, err := al.runAgentLoop(context.Background(), agent, processOptions{
		SessionKey: key, Channel: "telegram", ChatID: "chat-1", UserMessage: message,
	}); err != nil {
		t.Fatalf("turn %q on %s: %v", message, agent.Label(), err)
	}
}

// assertEmptyWorkspace fails when a fresh agent's workspace holds anything:
// no prompt files, skills or files/ are ever seeded into it.
func assertEmptyWorkspace(t *testing.T, agent *AgentInstance) {
	t.Helper()
	entries, err := os.ReadDir(agent.Workspace)
	if err != nil {
		t.Fatalf("fresh workspace %s: %v", agent.Workspace, err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("fresh workspace holds %v, want nothing", names)
	}
}

func cogmemDBExists(agent *AgentInstance) bool {
	_, err := os.Stat(cogmemstore.DBPath(cogmemhost.Dir(agent.StateDir)))
	return err == nil
}

func cogmemDirExists(agent *AgentInstance) bool {
	_, err := os.Stat(cogmemhost.Dir(agent.StateDir))
	return err == nil
}

// archiveFiles lists the conversation databases in agent's sessions dir.
func archiveFiles(t *testing.T, agent *AgentInstance) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(agent.StateDir, "sessions"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if strings.Contains(e.Name(), ".archive.db") {
			out = append(out, e.Name())
		}
	}
	return out
}

// withStore opens agent's cognitive-memory database for the test.
func withStore(t *testing.T, agent *AgentInstance, fn func(context.Context, *cogmemstore.Store)) {
	t.Helper()
	st, err := cogmemstore.Open(cogmemstore.DBPath(cogmemhost.Dir(agent.StateDir)))
	if err != nil {
		t.Fatalf("open %s's memory: %v", agent.Label(), err)
	}
	defer utils.CloseQuietly(st)
	fn(context.Background(), st)
}

// memorySessions counts the cached sessions of agent id and how many of them
// have a memory session.
func memorySessions(al *AgentLoop, id string) (sessions, withMemory int) {
	al.contextManagers.Range(func(key, v any) bool {
		if e, ok := v.(*cmEntry); ok && strings.HasPrefix(fmt.Sprint(key), id+":") {
			sessions++
			if e.mem != nil {
				withMemory++
			}
		}
		return true
	})
	return sessions, withMemory
}

// TestFreshAgent_Default: a fresh agent's whole system prompt is
// DefaultSystemPrompt (no identity, workspace files, skills, date, runtime or
// session token), it is offered no tools, its workspace stays empty, it keeps
// its conversation, and it has a working cognitive memory whose recall is
// placed after the prompt.
func TestFreshAgent_Default(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()
	al, model, _ := freshLoop(t)
	al.RegisterTool(&noopWriteFile{})

	id, err := al.GetRegistry().Create(config.AgentConfig{Tools: []string{"*"}, MCPTools: []string{"*"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	fresh, _ := al.GetRegistry().Get(id)
	if fresh.Spec.Mode != agentreg.ModeMemory || !fresh.Config.CognitiveMemoryEnabled() {
		t.Fatalf("default mode = %q, cogmem %v; want memory, on", fresh.Spec.Mode, fresh.Config.CognitiveMemoryEnabled())
	}
	assertEmptyWorkspace(t, fresh)

	turn(t, al, id, "first message")
	call := model.last(t)
	if got := call.systemPrompt(t); got != agentreg.DefaultSystemPrompt {
		t.Fatalf("system prompt = %q, want exactly %q", got, agentreg.DefaultSystemPrompt)
	}
	if len(call.tools) != 0 {
		t.Fatalf("a fresh agent was offered tools %v", call.tools)
	}
	if got := call.conversation(); !slices.Equal(got, []string{"user: first message"}) {
		t.Fatalf("turn 1 sent %v", got)
	}

	// Turn 2 sees turn 1.
	turn(t, al, id, "second message")
	if got, want := model.last(t).conversation(), []string{
		"user: first message", "assistant: reply 1", "user: second message",
	}; !slices.Equal(got, want) {
		t.Fatalf("turn 2 sent %v, want %v", got, want)
	}

	// Memory works: the turns were observed into its own store, and what is
	// in it is recalled after the prompt.
	if !cogmemDBExists(fresh) {
		t.Fatal("a fresh agent with memory has no memory database after a turn")
	}
	withStore(t, fresh, func(ctx context.Context, st *cogmemstore.Store) {
		if n, err := st.InboxCount(ctx, st.DB()); err != nil || n < 4 {
			t.Fatalf("memory inbox holds %d messages (%v), want the 4 of two turns", n, err)
		}
		general, err := st.GeneralDomain(ctx, st.DB())
		if err != nil {
			t.Fatalf("GeneralDomain: %v", err)
		}
		if _, err := st.AddMemory(ctx, st.DB(), cogmemstore.AddMemoryParams{
			DomainID: general.ID, Type: cogmemstore.TypeFact, Text: "Bob prefers tea.",
			Confidence: 1, Origin: cogmemstore.OriginUser,
		}); err != nil {
			t.Fatalf("AddMemory: %v", err)
		}
	})
	turn(t, al, id, "third message")
	sys := model.last(t).systemPrompt(t)
	// Exactly the prompt, the engine's separator, and the memory's stable
	// recall block: nothing else.
	_, mem, release := al.getSessionContext(fresh, routing.BuildAgentMainSessionKey(id))
	var stable string
	for _, inj := range mem.Recall(context.Background(), "third message") {
		if inj.Placement == cogmem.PlaceSystemStable {
			stable = inj.Text
		}
	}
	release()
	if stable == "" || !strings.Contains(stable, "Bob prefers tea.") {
		t.Fatalf("memory recall has no stable block with the memory: %q", stable)
	}
	if want := agentreg.DefaultSystemPrompt + "\n\n---\n\n" + stable; sys != want {
		t.Fatalf("system prompt\n%q\nwant\n%q", sys, want)
	}
	if strings.Contains(sys, "cogmem_") {
		t.Fatalf("the memory tools' guidance reached a fresh agent's prompt: %q", sys)
	}
	assertEmptyWorkspace(t, fresh)
}

// TestFreshAgent_WithSystemPrompt: the creator's prompt replaces the default
// and is the whole system prompt.
func TestFreshAgent_WithSystemPrompt(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()
	al, model, _ := freshLoop(t)

	const prompt = "You are Bob. Review the diff you are given and list the defects."
	id, err := al.GetRegistry().Create(config.AgentConfig{}, agentreg.WithSystemPrompt(prompt))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	turn(t, al, id, "diff")
	if got := model.last(t).systemPrompt(t); got != prompt {
		t.Fatalf("system prompt = %q, want exactly %q", got, prompt)
	}
}

// TestFreshAgent_WithoutMemory keeps its conversation but has no memory: no
// memory directory, nothing observed or recalled.
func TestFreshAgent_WithoutMemory(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()
	al, model, _ := freshLoop(t)

	id, err := al.GetRegistry().Create(config.AgentConfig{}, agentreg.WithoutMemory())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	fresh, _ := al.GetRegistry().Get(id)
	if fresh.Config.CognitiveMemoryEnabled() {
		t.Fatal("WithoutMemory left cognitive memory on")
	}
	turn(t, al, id, "first message")
	turn(t, al, id, "second message")
	call := model.last(t)
	if got, want := call.conversation(), []string{
		"user: first message", "assistant: reply 1", "user: second message",
	}; !slices.Equal(got, want) {
		t.Fatalf("turn 2 sent %v, want %v", got, want)
	}
	if got := call.systemPrompt(t); got != agentreg.DefaultSystemPrompt {
		t.Fatalf("system prompt = %q, want exactly the default", got)
	}
	if cogmemDirExists(fresh) {
		t.Fatal("an agent without memory has a memory directory")
	}
	if len(archiveFiles(t, fresh)) == 0 {
		t.Fatal("the conversation of an agent without memory was not kept")
	}
	if sessions, withMemory := memorySessions(al, id); sessions == 0 || withMemory != 0 {
		t.Fatalf("cached sessions %d, with memory %d; want some, none with memory", sessions, withMemory)
	}
}

// TestFreshAgent_SingleShot: every turn sees only the system prompt and its
// own message, nothing accumulates on disk, and there is no memory.
func TestFreshAgent_SingleShot(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()
	al, model, _ := freshLoop(t)

	id, err := al.GetRegistry().Create(config.AgentConfig{}, agentreg.SingleShot(), agentreg.WithSystemPrompt("Translate to French."))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	fresh, _ := al.GetRegistry().Get(id)
	if fresh.Config.CognitiveMemoryEnabled() || !fresh.Spec.SingleShot() {
		t.Fatalf("single-shot agent: cogmem %v, mode %q", fresh.Config.CognitiveMemoryEnabled(), fresh.Spec.Mode)
	}
	for i := range 5 {
		msg := fmt.Sprintf("message %d", i+1)
		turn(t, al, id, msg)
		call := model.last(t)
		if len(call.messages) != 2 || call.systemPrompt(t) != "Translate to French." ||
			!slices.Equal(call.conversation(), []string{"user: " + msg}) {
			t.Fatalf("turn %d sent %+v, want only the system prompt and %q", i+1, call.messages, msg)
		}
		if files := archiveFiles(t, fresh); len(files) != 0 {
			t.Fatalf("after turn %d the conversation is still on disk: %v", i+1, files)
		}
		if sessions, _ := memorySessions(al, id); sessions != 0 {
			t.Fatalf("after turn %d the session is still cached", i+1)
		}
	}
	if cogmemDirExists(fresh) {
		t.Fatal("a single-shot agent has a memory directory")
	}
	assertEmptyWorkspace(t, fresh)
}

// TestFreshAgent_SingleShotStartsBlankWhenHistoryRemains: a turn that finds
// history left in the session (its previous discard could not run) still
// starts on a blank context.
func TestFreshAgent_SingleShotStartsBlankWhenHistoryRemains(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()
	al, model, _ := freshLoop(t)

	id, err := al.GetRegistry().Create(config.AgentConfig{}, agentreg.SingleShot())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	fresh, _ := al.GetRegistry().Get(id)
	key := routing.BuildAgentMainSessionKey(id)
	if _, err := fresh.Sessions.AddFullMessage(key, providers.Message{Role: "user", Content: "left over"}); err != nil {
		t.Fatal(err)
	}
	turn(t, al, id, "new message")
	if got := model.last(t).conversation(); !slices.Equal(got, []string{"user: new message"}) {
		t.Fatalf("turn sent %v, want only the new message", got)
	}
}

// TestFreshAgent_ModesSurviveRestart: after a restart each fresh agent keeps
// its mode, prompt and owner, its workspace is still empty (the start-up
// seeding skips it), and it behaves as before.
func TestFreshAgent_ModesSurviveRestart(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()
	al, _, cfg := freshLoop(t)
	reg := al.GetRegistry()

	memID, err := reg.Create(config.AgentConfig{Name: "Bob"}, agentreg.WithSystemPrompt("You are Bob."), agentreg.OwnedBy("main"))
	if err != nil {
		t.Fatal(err)
	}
	noMemID, err := reg.Create(config.AgentConfig{}, agentreg.WithoutMemory())
	if err != nil {
		t.Fatal(err)
	}
	shotID, err := reg.Create(config.AgentConfig{}, agentreg.SingleShot())
	if err != nil {
		t.Fatal(err)
	}
	turn(t, al, memID, "before restart")
	turn(t, al, noMemID, "before restart")
	turn(t, al, shotID, "before restart")
	al.Close(context.Background())
	reg.Close()

	model := &recordingProvider{}
	restarted := mustNewAgentLoop(t, cfg, bus.NewMessageBus(), model, nil, OwnsDataDir())
	want := map[string]struct {
		mode   agentreg.Mode
		prompt string
		owner  string
	}{
		memID:   {agentreg.ModeMemory, "You are Bob.", "main"},
		noMemID: {agentreg.ModeNoMemory, agentreg.DefaultSystemPrompt, ""},
		shotID:  {agentreg.ModeSingleShot, agentreg.DefaultSystemPrompt, ""},
	}
	for id, w := range want {
		got, ok := restarted.GetRegistry().Get(id)
		if !ok {
			t.Fatalf("%s not restored", id)
		}
		if got.Spec.Mode != w.mode || got.Spec.SystemPrompt != w.prompt || got.Spec.Owner != w.owner ||
			got.Config.CognitiveMemoryEnabled() != (w.mode == agentreg.ModeMemory) {
			t.Fatalf("%s restored as mode %q prompt %q owner %q cogmem %v; want %+v",
				id, got.Spec.Mode, got.Spec.SystemPrompt, got.Spec.Owner, got.Config.CognitiveMemoryEnabled(), w)
		}
		assertEmptyWorkspace(t, got)
	}

	turn(t, restarted, memID, "after restart")
	call := model.last(t)
	if call.systemPrompt(t) != "You are Bob." || !slices.Contains(call.conversation(), "user: before restart") {
		t.Fatalf("the agent with memory lost its prompt or conversation: %+v", call.messages)
	}
	turn(t, restarted, noMemID, "after restart")
	if !slices.Contains(model.last(t).conversation(), "user: before restart") {
		t.Fatal("the agent without memory lost its conversation over the restart")
	}
	shot, _ := restarted.GetRegistry().Get(shotID)
	if cogmemDirExists(shot) {
		t.Fatal("a restored single-shot agent has a memory directory")
	}
	turn(t, restarted, shotID, "after restart")
	if got := model.last(t).conversation(); !slices.Equal(got, []string{"user: after restart"}) {
		t.Fatalf("the restored single-shot agent sent %v", got)
	}
}
