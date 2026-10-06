// ClawEh
// License: MIT

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/forum"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/providers"
	"github.com/PivotLLM/ClawEh/tools"
	toolsagents "github.com/PivotLLM/ClawEh/tools/agents"
	toolsforum "github.com/PivotLLM/ClawEh/tools/forum"
)

// forumToolNames are the ten tools an agent with the `forum` switch gets.
var forumToolNames = []string{
	"forum_readme", "forum_models", "forum_validate", "forum_launch", "forum_status", "forum_pause",
	"forum_resume", "forum_cancel", "forum_results", "forum_delete",
}

// freshPrompt is the fresh participant's system prompt in forumLaunchConfig,
// so the scripted model can tell its turns from Bob's.
const freshPrompt = "You are a careful reviewer."

// forumLaunchConfig has Bob (an existing agent) and a fresh temporary agent
// on alpha each give one view, Bob first.
const forumLaunchConfig = `{
  "version": 1, "name": "review",
  "brief": {"purpose": "Exercise the forum wiring.", "task": "Give one view."},
  "participants": {
    "bob": {"agent": "bob", "instructions": "Speak as yourself."},
    "fresh": {"model": "alpha", "system_prompt": "` + freshPrompt + `", "mode": "context"}
  },
  "limits": {"max_calls": 10, "max_duration_seconds": 600,
    "call_timeout_seconds": 60, "max_attempts_per_turn": 2, "max_parallel_calls": 1},
  "layers": [
    {"id": "talk", "participants": ["bob", "fresh"], "instructions": "Give one view.",
     "delivery": "per_turn", "max_rounds": 1, "output": {"format": "text"}}
  ]
}`

// forumConfig has Alice (default, allowed to target Bob) and Bob, both with
// the `forum` switch as given, on the configured model alpha.
func forumConfig(t *testing.T, aliceForum, bobForum bool) *config.Config {
	t.Helper()
	cfg := ownerTestConfig(t)
	cfg.Providers = []config.Provider{{Name: "p", Protocol: "openai-chat", BaseURL: "http://127.0.0.1:0/v1", APIKey: "k"}}
	cfg.Models = []config.ModelConfig{{ModelName: "alpha", Model: "alpha-wire", Provider: "p", Enabled: true}}
	cfg.Agents.Defaults.Models = []string{"alpha"}
	cfg.Agents.List = []config.AgentConfig{
		{ID: "alice", Name: "Alice", Default: true, Forum: aliceForum, Subagents: &config.SubagentsConfig{AllowAgents: []string{"bob"}}},
		{ID: "bob", Name: "Bob", Forum: bobForum},
	}
	return cfg
}

// forumRig is a running owner loop with the forum service wired as the
// gateway wires it.
type forumRig struct {
	bus  *bus.MessageBus
	al   *AgentLoop
	svc  *forum.Service
	host *ForumHost
	stop func()
}

// newForumRig builds the service, then the loop (which builds the forum
// tools), binds the host and dispatches inbound messages as Run does. stop
// closes the service first, as the gateway does, then the loop.
func newForumRig(t *testing.T, cfg *config.Config, model providers.LLMProvider) *forumRig {
	t.Helper()
	tools.RegisterProvider(tools.NamespacedProvider(toolsforum.Suite, toolsforum.GlobalProvider))
	host := NewForumHost()
	svc := forum.New(forum.Host{
		Messenger: host, Agents: host, Notifier: host, Logger: logger.NewLogger("forum"),
		Schemas: forum.JSONSchemaValidator{}, OnStuck: host.OnStuck,
	})
	toolsforum.SetService(svc)
	msgBus := bus.NewMessageBus()
	al, err := NewAgentLoop(cfg, msgBus, model, nil, OwnsDataDir())
	if err != nil {
		t.Fatalf("NewAgentLoop: %v", err)
	}
	host.Bind(al)
	ctx, cancel := context.WithCancel(context.Background())
	al.running.Store(true)
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			msg, ok := msgBus.ConsumeInbound(ctx)
			if !ok {
				return
			}
			al.dispatchInbound(ctx, msg)
		}
	})
	var once sync.Once
	stop := func() {
		once.Do(func() {
			closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer closeCancel()
			if err := svc.Close(closeCtx); err != nil {
				t.Errorf("forum service Close: %v", err)
			}
			al.running.Store(false)
			cancel()
			wg.Wait()
			al.activeRequests.Wait()
			al.Close(closeCtx)
		})
	}
	t.Cleanup(stop)
	return &forumRig{bus: msgBus, al: al, svc: svc, host: host, stop: stop}
}

// forumModel is the scripted model of every agent in the forum tests. Bob's
// forum turn calls forum_status (refused inside a forum turn) and then
// replies; the fresh participant replies at once; anything else is noted.
type forumModel struct {
	mu        sync.Mutex
	users     []string // the last user message of every call
	askDepths []int    // the sub-agent depth of every forum turn
	toolSeen  []string // the tool results Bob's turns received
	block     bool     // forum turns wait for their context to end
	bobTool   string   // the tool Bob's forum turn calls (forum_status when empty)
	bobArgs   string   // its JSON arguments ({} when empty)
}

func (m *forumModel) GetDefaultModel() string { return "mock-model" }

func (m *forumModel) Chat(ctx context.Context, messages []providers.Message, _ []providers.ToolDefinition, _ string, _ map[string]any) (*providers.LLMResponse, error) {
	user := lastUser(messages)
	last := messages[len(messages)-1]
	forumTurn := strings.Contains(user, "[Message from Alice — Alice is waiting for your reply]")
	m.mu.Lock()
	m.users = append(m.users, user)
	if forumTurn && last.Role != "tool" {
		m.askDepths = append(m.askDepths, toolsagents.SpawnDepth(ctx))
	}
	block := m.block
	m.mu.Unlock()
	switch {
	case forumTurn && block:
		<-ctx.Done()
		return nil, ctx.Err()
	case last.Role == "tool":
		m.mu.Lock()
		m.toolSeen = append(m.toolSeen, last.Content)
		m.mu.Unlock()
		return &providers.LLMResponse{Content: "Bob's view."}, nil
	case forumTurn && strings.Contains(messages[0].Content, freshPrompt):
		return &providers.LLMResponse{Content: "The reviewer's view."}, nil
	case forumTurn:
		tool, args := "forum_status", "{}"
		if m.bobTool != "" {
			tool, args = m.bobTool, m.bobArgs
		}
		return &providers.LLMResponse{ToolCalls: []providers.ToolCall{{
			ID: "tc-1", Type: "function", Name: tool,
			Function: &providers.FunctionCall{Name: tool, Arguments: args},
		}}}, nil
	}
	return &providers.LLMResponse{Content: "Noted."}, nil
}

// sawUser reports whether some call's last user message contains s.
func (m *forumModel) sawUser(s string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.ContainsFunc(m.users, func(u string) bool { return strings.Contains(u, s) })
}

// eventually fails the test unless cond holds within ten seconds.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// launchForum runs forum_launch as Alice from channel with
// forumLaunchConfig and returns the new forum's id.
func launchForum(t *testing.T, al *AgentLoop, channel string) string {
	t.Helper()
	return launchForumWith(t, al, channel, forumLaunchConfig)
}

// launchForumWith is launchForum with the forum configuration launchConfig.
func launchForumWith(t *testing.T, al *AgentLoop, channel, launchConfig string) string {
	t.Helper()
	alice, _ := al.GetRegistry().Get("alice")
	var cfgObj map[string]any
	if err := json.Unmarshal([]byte(launchConfig), &cfgObj); err != nil {
		t.Fatal(err)
	}
	res := alice.Tools.ExecuteWithContext(context.Background(), "forum_launch", map[string]any{"config": cfgObj}, channel, "chat-1", nil)
	if res.IsError || !strings.HasPrefix(res.ForLLM, "Forum ") || !strings.HasSuffix(res.ForLLM, " launched.") {
		t.Fatalf("forum_launch = %+v", res)
	}
	return strings.TrimSuffix(strings.TrimPrefix(res.ForLLM, "Forum "), " launched.")
}

// aliceScope is Alice's forum scope.
func aliceScope(t *testing.T, al *AgentLoop) forum.Scope {
	t.Helper()
	alice, _ := al.GetRegistry().Get("alice")
	base, err := filepath.Abs(filepath.Join(alice.Workspace, "forums"))
	if err != nil {
		t.Fatal(err)
	}
	return forum.Scope{AgentID: "alice", BaseDirectory: base}
}

// waitCompleted waits for forum id to complete and returns its result.
func waitCompleted(t *testing.T, r *forumRig, id string) *forum.Result {
	t.Helper()
	var res *forum.Result
	eventually(t, "the forum to complete", func() bool {
		got, err := r.svc.Results(context.Background(), aliceScope(t, r.al), id)
		if err == nil && got.Status.Terminal() {
			res = got
			return true
		}
		return false
	})
	if res.Status != forum.StatusCompleted {
		t.Fatalf("forum ended %s (%s), want completed", res.Status, res.Reason)
	}
	return res
}

// TestForum_EndToEnd: Alice launches a forum of Bob and a fresh temporary
// agent through forum_launch. Each turn is a core ask from Alice at the
// maximum sub-agent depth, where forum tools are refused; the transcript is
// written under Alice's workspace, the temporary agent is deleted and Alice
// gets the completion notice in her conversation.
func TestForum_EndToEnd(t *testing.T) {
	t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))
	cfg := forumConfig(t, true, true)
	model := &forumModel{}
	r := newForumRig(t, cfg, model)

	id := launchForum(t, r.al, "telegram")
	res := waitCompleted(t, r, id)
	if len(res.Layers) != 1 || len(res.Layers[0].Outputs) != 2 {
		t.Fatalf("result = %+v, want two outputs", res)
	}

	alice, _ := r.al.GetRegistry().Get("alice")
	transcript, err := os.ReadFile(filepath.Join(alice.Workspace, "forums", id, "transcript.md"))
	if err != nil {
		t.Fatalf("transcript: %v", err)
	}
	for _, want := range []string{"Bob's view.", "The reviewer's view."} {
		if !strings.Contains(string(transcript), want) {
			t.Errorf("transcript lacks %q:\n%s", want, transcript)
		}
	}

	model.mu.Lock()
	depths, seen := slices.Clone(model.askDepths), slices.Clone(model.toolSeen)
	model.mu.Unlock()
	maxDepth := cfg.Agents.Defaults.GetMaxSubagentDepth()
	if len(depths) != 2 || depths[0] != maxDepth || depths[1] != maxDepth {
		t.Errorf("forum turn depths = %v, want two turns at the maximum depth %d", depths, maxDepth)
	}
	if len(seen) != 1 || !strings.Contains(seen[0], "Forum tools are not available at the maximum sub-agent depth.") {
		t.Errorf("Bob's forum_status inside the forum turn returned %q, want the maximum-depth refusal", seen)
	}

	eventually(t, "the temporary participant's deletion", func() bool { return len(r.al.GetRegistry().ListTemp()) == 0 })
	eventually(t, "Alice's completion notice", func() bool {
		return model.sawUser("[System: forum] Forum review finished: completed (id " + id + ").")
	})
}

// TestForum_ToolsFollowTheSwitch: an agent gets the ten forum tools only
// with its `forum` switch on.
func TestForum_ToolsFollowTheSwitch(t *testing.T) {
	t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))
	r := newForumRig(t, forumConfig(t, true, false), &forumModel{})
	alice, _ := r.al.GetRegistry().Get("alice")
	bob, _ := r.al.GetRegistry().Get("bob")
	for _, name := range forumToolNames {
		if _, ok := alice.Tools.Get(name); !ok {
			t.Errorf("alice (forum on) lacks %s", name)
		}
		if _, ok := bob.Tools.Get(name); ok {
			t.Errorf("bob (forum off) has %s", name)
		}
	}
}

// TestForum_RecoverResumesAfterRestart: a forum interrupted by a shutdown in
// the middle of Bob's turn resumes after a restart, through Recover over
// every agent (Alice's switch is turned off meanwhile: it gates only her
// tools), and completes with its temporary agent restored and then deleted.
func TestForum_RecoverResumesAfterRestart(t *testing.T) {
	t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))
	cfg := forumConfig(t, true, false)
	first := &forumModel{block: true}
	r1 := newForumRig(t, cfg, first)
	id := launchForum(t, r1.al, "cli")
	eventually(t, "Bob's forum turn", func() bool {
		first.mu.Lock()
		defer first.mu.Unlock()
		return len(first.askDepths) > 0
	})
	tempIDs := r1.al.GetRegistry().ListTemp()
	if len(tempIDs) != 1 {
		t.Fatalf("temporary agents = %v, want the fresh participant", tempIDs)
	}
	r1.stop()

	second := &forumModel{}
	cfg.Agents.List[0].Forum = false
	r2 := newForumRig(t, cfg, second)
	if alice, _ := r2.al.GetRegistry().Get("alice"); alice != nil {
		if _, ok := alice.Tools.Get("forum_status"); ok {
			t.Fatal("alice has forum tools with her switch off")
		}
	}
	if !slices.Equal(r2.al.GetRegistry().ListTemp(), tempIDs) {
		t.Fatalf("temporary agents after the restart = %v, want %v restored", r2.al.GetRegistry().ListTemp(), tempIDs)
	}
	scopes := r2.host.Scopes()
	if len(scopes) != 2 || scopes[0] != aliceScope(t, r2.al) {
		t.Fatalf("recovery scopes = %+v, want Alice's and Bob's", scopes)
	}
	if err := r2.svc.Recover(context.Background(), scopes); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	waitCompleted(t, r2, id)
	eventually(t, "the temporary participant's deletion", func() bool { return len(r2.al.GetRegistry().ListTemp()) == 0 })
	if !second.sawUser("Give one view.") {
		t.Error("Bob's interrupted turn was not resent after the restart")
	}
}

// TestForumHost_AskShutdown: an ask made while the loop is not running is a
// shutdown, so the forum leaves the attempt uncertain instead of failing it.
func TestForumHost_AskShutdown(t *testing.T) {
	t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))
	h := NewForumHost()
	if _, err := h.Ask(context.Background(), "bob", "hello", time.Second); !errors.Is(err, forum.ErrShuttingDown) {
		t.Fatalf("Ask before Bind = %v, want ErrShuttingDown", err)
	}
	r := newForumRig(t, forumConfig(t, true, false), &forumModel{})
	r.al.running.Store(false)
	if _, err := r.host.Ask(context.Background(), "bob", "hello", time.Second); !errors.Is(err, forum.ErrShuttingDown) {
		t.Fatalf("Ask while stopped = %v, want ErrShuttingDown", err)
	}
}

// TestForumHost_Agents: the registry adapter checks allow_agents, describes
// the models, marks what it creates as forum participants owned by the
// launcher and treats deleting a gone agent as done.
func TestForumHost_Agents(t *testing.T) {
	t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))
	r := newForumRig(t, forumConfig(t, true, false), &forumModel{})
	ctx := context.Background()
	h := r.host
	if ok, err := h.MayTarget(ctx, "alice", "bob"); err != nil || !ok {
		t.Errorf("MayTarget(alice, bob) = %v, %v; bob is in her allow_agents", ok, err)
	}
	if ok, err := h.MayTarget(ctx, "bob", "alice"); err != nil || ok {
		t.Errorf("MayTarget(bob, alice) = %v, %v; bob has no allow_agents", ok, err)
	}
	models, err := h.Models(ctx, "alice")
	if err != nil || len(models) != 1 || models[0] != (forum.ModelInfo{Name: "alpha", Provider: "p", Protocol: "openai-chat"}) {
		t.Fatalf("Models(alice) = %+v, %v", models, err)
	}
	clone, err := h.CreateClone(ctx, forum.CloneSpec{Source: "bob", Model: "alpha", Owner: "alice"})
	if err != nil {
		t.Fatalf("CreateClone: %v", err)
	}
	fresh, err := h.CreateFresh(ctx, forum.FreshSpec{Model: "alpha", Mode: forum.FreshModeSingleShot, Owner: "alice"})
	if err != nil {
		t.Fatalf("CreateFresh: %v", err)
	}
	for _, id := range []string{clone, fresh} {
		a, ok := r.al.GetRegistry().Get(id)
		if !ok || a.Spec.Purpose != tools.TempPurposeForum || a.Spec.Owner != "alice" {
			t.Errorf("%s: spec = %+v, want a forum participant owned by alice", id, a.Spec)
		}
		if ok, err := h.Exists(ctx, id); err != nil || !ok {
			t.Errorf("Exists(%s) = %v, %v", id, ok, err)
		}
		if err := h.Touch(ctx, "bob", id); err == nil {
			t.Errorf("Touch(%s) for launcher bob succeeded; alice owns it", id)
		}
		if err := h.Delete(ctx, "bob", id); err == nil {
			t.Errorf("Delete(%s) for launcher bob succeeded; alice owns it", id)
		}
		if err := h.Touch(ctx, "alice", id); err != nil {
			t.Errorf("Touch(%s): %v", id, err)
		}
		if err := h.Delete(ctx, "alice", id); err != nil {
			t.Errorf("Delete(%s): %v", id, err)
		}
		if err := h.Delete(ctx, "alice", id); err != nil {
			t.Errorf("Delete(%s) again = %v, want nil for an agent already gone", id, err)
		}
	}
	if _, err := h.CreateClone(ctx, forum.CloneSpec{Source: "alice", Owner: "bob"}); err == nil {
		t.Error("bob cloned alice without allow_agents")
	}
}

// TestForum_ShellFollowsTheParticipant: a forum turn runs with the
// participant's own shell_exec permission, wherever the forum was launched
// from: Bob, or a clone of Bob, runs it when Bob's tools allow it and is
// refused, by name, when they do not.
func TestForum_ShellFollowsTheParticipant(t *testing.T) {
	cloneConfig := strings.Replace(forumLaunchConfig, `"bob": {"agent": "bob",`, `"bob": {"clone": "bob",`, 1)
	if cloneConfig == forumLaunchConfig {
		t.Fatal("the clone configuration is the same as the existing-agent one")
	}
	for _, tc := range []struct {
		name     string
		launch   string
		bobTools []string
		want     string
	}{
		{"existing, allowed", forumLaunchConfig, []string{"*", "shell_exec"}, "forum-shell-ok"},
		{"existing, not allowed", forumLaunchConfig, []string{"*"}, "Bob is not allowed to run shell commands."},
		{"clone, allowed", cloneConfig, []string{"*", "shell_exec"}, "forum-shell-ok"},
		{"clone, not allowed", cloneConfig, []string{"*"}, "Bob is not allowed to run shell commands."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))
			model := &forumModel{bobTool: "shell_exec", bobArgs: `{"command":"echo forum-shell-ok"}`}
			cfg := forumConfig(t, true, false)
			cfg.Agents.List[1].Tools = tc.bobTools
			r := newForumRig(t, cfg, model)

			id := launchForumWith(t, r.al, "telegram", tc.launch)
			waitCompleted(t, r, id)
			model.mu.Lock()
			defer model.mu.Unlock()
			if len(model.toolSeen) != 1 || !strings.Contains(model.toolSeen[0], tc.want) ||
				(strings.Contains(tc.want, "not allowed") && model.toolSeen[0] != tc.want) {
				t.Errorf("Bob's shell_exec in the forum returned %q, want %q", model.toolSeen, tc.want)
			}
		})
	}
}

// TestForum_FreshParticipantHasNoShell: a fresh temporary agent has no
// tools, so it can never run shell commands, even when its owner can.
func TestForum_FreshParticipantHasNoShell(t *testing.T) {
	t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))
	cfg := forumConfig(t, true, false)
	cfg.Agents.List[0].Tools = []string{"*", "shell_exec"}
	r := newForumRig(t, cfg, &forumModel{})
	id, err := r.host.CreateFresh(context.Background(), forum.FreshSpec{Model: "alpha", Owner: "alice"})
	if err != nil {
		t.Fatalf("CreateFresh: %v", err)
	}
	fresh, ok := r.al.GetRegistry().Get(id)
	if !ok {
		t.Fatal("the fresh agent is not registered")
	}
	res := fresh.Tools.ExecuteWithContext(context.Background(), "shell_exec",
		map[string]any{"command": "echo forum-shell-ok"}, "telegram", "chat-1", nil)
	if !res.IsError || !strings.HasSuffix(res.ForLLM, "is not allowed to run shell commands.") {
		t.Errorf("fresh agent's shell_exec = %+v, want a refusal", res)
	}
}

// TestForumHost_AskChecksTheLauncherNow: the forum's records live in the
// launcher's workspace, so an ask goes only to an agent the launcher may
// target now, or to a forum participant it owns; an edited participants
// file cannot reach anyone else.
func TestForumHost_AskChecksTheLauncherNow(t *testing.T) {
	t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))
	r := newForumRig(t, forumConfig(t, true, false), &forumModel{})
	ctx := context.Background()
	asAlice := forum.WithAskInfo(ctx, forum.AskInfo{ForumID: "f1", Origin: forum.Origin{AgentID: "alice"}})
	asBob := forum.WithAskInfo(ctx, forum.AskInfo{ForumID: "f1", Origin: forum.Origin{AgentID: "bob"}})

	participant, err := r.host.CreateFresh(ctx, forum.FreshSpec{Model: "alpha", Owner: "alice"})
	if err != nil {
		t.Fatalf("CreateFresh: %v", err)
	}
	plainTemp, err := newAgentServices(r.al, "alice").CreateFresh("alpha")
	if err != nil {
		t.Fatalf("CreateFresh (not a forum participant): %v", err)
	}
	for _, tc := range []struct {
		name   string
		ctx    context.Context
		target string
		ok     bool
	}{
		{"alice asks bob (allowed)", asAlice, "bob", true},
		{"alice asks her own participant", asAlice, participant, true},
		{"bob asks alice (not in his allow list)", asBob, "alice", false},
		{"bob asks alice's participant", asBob, participant, false},
		{"alice asks a temporary agent that is not a forum participant", asAlice, plainTemp, false},
		{"an ask with no forum", ctx, "bob", false},
	} {
		reply, err := r.host.Ask(tc.ctx, tc.target, "hello", 10*time.Second)
		if tc.ok && (err != nil || reply.Outcome != forum.OutcomeOK) {
			t.Errorf("%s: %+v, %v; want an answer", tc.name, reply, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("%s: answered %+v; want a refusal", tc.name, reply)
		}
	}
	if err := r.host.Delete(ctx, "alice", plainTemp); err == nil {
		t.Error("Delete removed a temporary agent that is not a forum participant")
	}
	if err := r.host.Touch(ctx, "alice", plainTemp); err == nil {
		t.Error("Touch refreshed a temporary agent that is not a forum participant")
	}
	if _, ok := r.al.GetRegistry().Get(plainTemp); !ok {
		t.Error("the temporary agent that is not a forum participant was deleted")
	}
}

// TestForumHost_NoticeRouting: the completion notice always lands in the
// launcher's own conversation. The chat recorded at launch is not used: a
// launch from a chat has the answer posted to the launcher's default chat when
// it has one, and nowhere otherwise; a local launch is never posted.
func TestForumHost_NoticeRouting(t *testing.T) {
	result := &forum.Result{ForumID: "f1", Name: "review", Status: forum.StatusCompleted}
	for _, tc := range []struct {
		name     string
		binding  bool
		origin   forum.Origin
		wantChat string // "" = nothing posted
	}{
		{"chat, default chat", true, forum.Origin{AgentID: "alice", Channel: "telegram", ChatID: "elsewhere"}, "u1"},
		{"chat, no default chat", false, forum.Origin{AgentID: "alice", Channel: "telegram", ChatID: "elsewhere"}, ""},
		{"local", true, forum.Origin{AgentID: "alice", Channel: "cli", ChatID: "elsewhere"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))
			cfg := forumConfig(t, true, false)
			if tc.binding {
				cfg.Bindings = []config.AgentBinding{{
					AgentID: "alice", Default: true,
					Match: config.BindingMatch{Channel: "telegram", Peer: &config.PeerMatch{Kind: "direct", ID: "u1"}},
				}}
			}
			model := &forumModel{}
			r := newForumRig(t, cfg, model)
			if err := r.host.ForumFinished(context.Background(), tc.origin, result); err != nil {
				t.Fatalf("ForumFinished: %v", err)
			}
			eventually(t, "the notice in Alice's conversation", func() bool {
				return model.sawUser("[System: forum] Forum review finished: completed (id f1).")
			})
			if tc.wantChat == "" {
				noOutbound(t, r.bus)
				return
			}
			out := nextOutbound(t, r.bus)
			if out.Channel != "telegram" || out.ChatID != tc.wantChat {
				t.Errorf("answer posted to %s/%s, want telegram/%s", out.Channel, out.ChatID, tc.wantChat)
			}
		})
	}
}

// TestForumHost_ScopesIncludeEveryAgent: recovery runs over every configured
// agent, whatever its switch, so forums of an agent whose switch was turned
// off are still resumed, kept alive and cleaned up.
func TestForumHost_ScopesIncludeEveryAgent(t *testing.T) {
	t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))
	r := newForumRig(t, forumConfig(t, false, false), &forumModel{})
	scopes := r.host.Scopes()
	ids := make([]string, 0, len(scopes))
	for _, s := range scopes {
		ids = append(ids, s.AgentID)
	}
	if !slices.Equal(ids, []string{"alice", "bob"}) {
		t.Fatalf("scopes = %v, want every configured agent", ids)
	}
}
