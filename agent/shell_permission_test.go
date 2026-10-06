// ClawEh
// License: MIT

package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/agentreg"
	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/providers"
	"github.com/PivotLLM/ClawEh/tools"
	toolsagents "github.com/PivotLLM/ClawEh/tools/agents"
)

// Whether an agent may run shell commands is its own tool permission for
// shell_exec and nothing else: it holds on every channel and for everything
// acting as the agent. Every test below has an allowed case (Alice) and a
// denied case (Bob), and checks the exact refusal naming the agent.

const (
	shellArgs   = `{"command":"echo shell-ok"}`
	shellOutput = "shell-ok"
)

// noShell is the refusal of a shell_exec call by the agent named name.
func noShell(name string) string { return name + " is not allowed to run shell commands." }

// shellModel calls shell_exec on every turn, records the result it gets
// back and replies "done: <result>".
type shellModel struct {
	mu      sync.Mutex
	results []string
}

func (m *shellModel) GetDefaultModel() string { return "mock-model" }

func (m *shellModel) Chat(_ context.Context, messages []providers.Message, _ []providers.ToolDefinition, _ string, _ map[string]any) (*providers.LLMResponse, error) {
	last := messages[len(messages)-1]
	if last.Role == "tool" {
		m.mu.Lock()
		m.results = append(m.results, last.Content)
		m.mu.Unlock()
		return &providers.LLMResponse{Content: "done: " + last.Content}, nil
	}
	return &providers.LLMResponse{ToolCalls: []providers.ToolCall{{
		ID: "tc-1", Type: "function", Name: "shell_exec",
		Function: &providers.FunctionCall{Name: "shell_exec", Arguments: shellArgs},
	}}}, nil
}

// take returns the results recorded since the last take.
func (m *shellModel) take() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := m.results
	m.results = nil
	return out
}

// shellConfig is messagingConfig with shell_exec switched on for the
// install, allowed for Alice and left out of Bob's tools. Helper may target
// everyone and has agent_message.
func shellConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := messagingConfig(t)
	cfg.Tools.Overrides["shell_exec"] = true
	cfg.Agents.List[0].Tools = []string{"*"}
	cfg.Agents.List[1].Tools = []string{"file_*"}
	return cfg
}

// shellCases are the two sides of every check: Alice may run shell commands,
// Bob may not.
var shellCases = []struct {
	id, name string
	allowed  bool
}{
	{"alice", "Alice", true},
	{"bob", "Bob", false},
}

// checkShell fails the test unless got is the outcome of one shell_exec call
// by the agent named name: its output when allowed, else exactly the refusal
// naming it. A "done: " reply prefix is ignored.
func checkShell(t *testing.T, what, got, name string, allowed bool) {
	t.Helper()
	got = strings.TrimPrefix(got, "done: ")
	if allowed {
		if !strings.Contains(got, shellOutput) || strings.Contains(got, "not allowed") {
			t.Errorf("%s: %q, want the shell output %q", what, got, shellOutput)
		}
		return
	}
	if got != noShell(name) {
		t.Errorf("%s: %q, want exactly %q", what, got, noShell(name))
	}
}

// nextOutboundTo returns the next reply published to chatID, skipping any
// other (a background result's turn also publishes its final reply on the
// system channel, which no chat receives).
func nextOutboundTo(t *testing.T, msgBus *bus.MessageBus, chatID string) bus.OutboundMessage {
	t.Helper()
	for {
		if out := nextOutbound(t, msgBus); out.ChatID == chatID {
			return out
		}
	}
}

// checkOneResult checks the single shell_exec result model recorded.
func checkOneResult(t *testing.T, what string, model *shellModel, name string, allowed bool) {
	t.Helper()
	got := model.take()
	if len(got) != 1 {
		t.Fatalf("%s: %d shell_exec results %q, want one", what, len(got), got)
	}
	checkShell(t, what, got[0], name, allowed)
}

// TestShell_EveryChannel: a direct turn reaches the agent's shell_exec when
// it has it, and is refused by name when it does not, on every channel kind:
// cli, a chat (Telegram), the WebUI chat, a device, a cron job, a recovered
// turn and a background result re-entering as a system message.
func TestShell_EveryChannel(t *testing.T) {
	t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))
	model := &shellModel{}
	al, msgBus := messagingLoop(t, shellConfig(t), model)

	n := 0
	for _, kind := range []string{"cli", "telegram", "webui", "device", "cron", "recovery", "system"} {
		for _, a := range shellCases {
			n++
			id := "m" + strings.Repeat("x", n)
			msg := inbound("chat-"+a.id, id, "run it")
			msg.Metadata = map[string]string{metadataKeyPreresolvedAgentID: a.id}
			switch kind {
			case "cron":
				msg.Channel, msg.SenderID = "telegram", "cron"
			case "recovery":
				msg.Channel, msg.SenderID, msg.IsRetry = "telegram", "recovery", true
				msg.SessionKey = "agent:" + a.id + ":main"
			case "system":
				msg.Channel, msg.SenderID = "system", "async:agent_spawn"
				msg.ChatID = "telegram:chat-" + a.id
			default:
				msg.Channel = kind
			}
			dispatch(al, msg)
			out := nextOutboundTo(t, msgBus, "chat-"+a.id)
			checkShell(t, a.id+" on "+kind, out.Content, a.name, a.allowed)
			model.take()
		}
	}
}

// TestShell_BackgroundResultReentry: a background task's result re-entering
// the owner's conversation runs with the owner's permission.
func TestShell_BackgroundResultReentry(t *testing.T) {
	t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))
	model := &shellModel{}
	al, msgBus := messagingLoop(t, shellConfig(t), model)

	for _, a := range shellCases {
		al.taskPointerCallback("telegram", "chat-"+a.id, a.id, 0)(context.Background(), &tools.ToolResult{ForLLM: "task finished"})
		out := nextOutboundTo(t, msgBus, "chat-"+a.id)
		checkShell(t, a.id+" re-entry", out.Content, a.name, a.allowed)
		model.take()
	}
}

// TestShell_ActingAsTheAgent: everything that acts as an agent inherits its
// permission: agent_spawn sub-agents (wait and background), a Maestro
// dispatch worker, a clone made through AgentServices, and a turn asked
// through agent_message or Ask.
func TestShell_ActingAsTheAgent(t *testing.T) {
	t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))
	model := &shellModel{}
	al, _ := messagingLoop(t, shellConfig(t), model)
	ctx := context.Background()

	for _, a := range shellCases {
		inst, _ := al.GetRegistry().Get(a.id)
		if inst.spawnMgr == nil {
			t.Fatalf("%s has no sub-agent manager", a.id)
		}

		res, err := inst.spawnMgr.Run(ctx, "run it", "job", "", "cli", "direct", "", nil)
		if err != nil {
			t.Fatalf("%s agent_spawn wait: %v", a.id, err)
		}
		if res.IsError {
			t.Fatalf("%s agent_spawn wait: %s", a.id, res.ForLLM)
		}
		checkOneResult(t, a.id+" agent_spawn wait", model, a.name, a.allowed)

		done := make(chan *tools.ToolResult, 1)
		if _, err = inst.spawnMgr.SpawnCallback("run it", "job", "", "cli", "direct", "", nil,
			func(_ context.Context, r *tools.ToolResult) { done <- r }, 0); err != nil {
			t.Fatalf("%s agent_spawn background: %v", a.id, err)
		}
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("%s agent_spawn background: no result", a.id)
		}
		checkOneResult(t, a.id+" agent_spawn background", model, a.name, a.allowed)

		sr, err := toolsagents.NewSpawner(inst.spawnMgr).RunSync(ctx, "run it", "")
		if err != nil {
			t.Fatalf("%s Maestro dispatch: %v", a.id, err)
		}
		checkShell(t, a.id+" Maestro dispatch", sr.Content, a.name, a.allowed)
		model.take()

		cloneID, err := newAgentServices(al, "helper").CreateClone(a.id)
		if err != nil {
			t.Fatalf("clone %s: %v", a.id, err)
		}
		reply, err := al.Ask(ctx, "Helper", cloneID, "run it", 5*time.Second)
		if err != nil {
			t.Fatalf("ask the clone of %s: %v", a.id, err)
		}
		checkShell(t, a.id+" clone (AgentServices)", reply.Text, a.name, a.allowed)
		model.take()

		tr := callMessageTool(t, al, "helper", a.id, float64(5))
		if tr.IsError {
			t.Fatalf("agent_message to %s: %s", a.id, tr.ForLLM)
		}
		checkOneResult(t, a.id+" agent_message", model, a.name, a.allowed)

		reply, err = al.Ask(ctx, "Helper", a.id, "run it", 5*time.Second)
		if err != nil {
			t.Fatalf("Ask %s: %v", a.id, err)
		}
		if reply.Outcome != bus.OutcomeOK {
			t.Fatalf("Ask %s: %+v", a.id, reply)
		}
		checkShell(t, a.id+" Ask", reply.Text, a.name, a.allowed)
		model.take()
	}
}

// TestShell_FreshAgentNever: a fresh temporary agent has no tools: asked to
// run a command, it is refused by name, even when its creator may run them.
// An unnamed one is called "temporary agent <short id>", never its UUID.
func TestShell_FreshAgentNever(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts []tools.FreshOption
		want func(id string) string
	}{
		{"named", []tools.FreshOption{tools.WithName("Bob")}, func(string) string { return "Bob" }},
		{"unnamed", nil, func(id string) string { return "temporary agent " + agentreg.ShortID(id) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))
			cfg := forumConfig(t, true, false)
			cfg.Tools.Overrides = map[string]bool{"shell_exec": true}
			cfg.Agents.List[0].Tools = []string{"*"}
			model := &shellModel{}
			r := newForumRig(t, cfg, model)
			id, err := newAgentServices(r.al, "alice").CreateFresh("alpha", tc.opts...)
			if err != nil {
				t.Fatalf("CreateFresh: %v", err)
			}
			want := tc.want(id)
			reply, err := r.al.Ask(context.Background(), "Alice", id, "run it", 5*time.Second)
			if err != nil {
				t.Fatalf("Ask: %v", err)
			}
			checkShell(t, "fresh agent", reply.Text, want, false)
			checkOneResult(t, "fresh agent", model, want, false)
		})
	}
}

// TestShell_UnnamedCloneUsesSourceName: a clone of an agent with no name is
// refused under its source's display name (the source's id), not the
// clone's UUID.
func TestShell_UnnamedCloneUsesSourceName(t *testing.T) {
	t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))
	cfg := shellConfig(t)
	cfg.Agents.List[1].Name = "" // Bob has no name: his display name is "bob"
	model := &shellModel{}
	al, _ := messagingLoop(t, cfg, model)

	cloneID, err := newAgentServices(al, "helper").CreateClone("bob")
	if err != nil {
		t.Fatalf("clone bob: %v", err)
	}
	reply, err := al.Ask(context.Background(), "Helper", cloneID, "run it", 5*time.Second)
	if err != nil {
		t.Fatalf("ask the clone: %v", err)
	}
	checkShell(t, "unnamed clone", reply.Text, "bob", false)
	checkOneResult(t, "unnamed clone", model, "bob", false)
}

// TestShell_HumanAgentNever: an agent that stands for a person has no tools,
// so it can never run shell commands, whatever its tool list says.
func TestShell_HumanAgentNever(t *testing.T) {
	t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))
	cfg := humanMessagingConfig(t, 60)
	cfg.Tools.Overrides["shell_exec"] = true
	for i := range cfg.Agents.List {
		cfg.Agents.List[i].Tools = []string{"*"}
	}
	al, _ := humanMessagingLoop(t, cfg, &countingProvider{})
	bob, _ := al.GetRegistry().Get("bob")
	if bob.HumanModel == "" {
		t.Fatal("bob is not a human agent: the check proves nothing")
	}
	if n := bob.Tools.Count(); n != 0 {
		t.Errorf("the human agent has %d tools, want none", n)
	}
	for _, res := range []*tools.ToolResult{
		bob.Tools.ExecuteWithContext(context.Background(), "shell_exec",
			map[string]any{"command": "echo shell-ok"}, "telegram", "chat-1", nil),
		bob.Tools.ExecuteForHost(context.Background(), "shell_exec",
			map[string]any{"command": "echo shell-ok"}, "telegram", "chat-1", nil),
	} {
		if !res.IsError {
			t.Errorf("the human agent ran shell_exec: %+v", res)
		}
		checkShell(t, "human agent", res.ForLLM, "Bob", false)
	}
}

// TestShell_ConfigSwitches: tools, deny_tools and tool_overrides decide it
// as before.
func TestShell_ConfigSwitches(t *testing.T) {
	for _, tc := range []struct {
		name    string
		edit    func(cfg *config.Config)
		allowed bool
	}{
		{"tools *", func(*config.Config) {}, true},
		{"tools lists shell_exec", func(c *config.Config) { c.Agents.List[0].Tools = []string{"shell_exec"} }, true},
		{"tools leave it out", func(c *config.Config) { c.Agents.List[0].Tools = []string{"file_*"} }, false},
		{"deny_tools", func(c *config.Config) { c.Agents.List[0].DenyTools = []string{"shell_exec"} }, false},
		{"tool_overrides off", func(c *config.Config) { c.Tools.Overrides["shell_exec"] = false }, false},
		{"tool_overrides unset", func(c *config.Config) { delete(c.Tools.Overrides, "shell_exec") }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))
			cfg := shellConfig(t)
			tc.edit(cfg)
			model := &shellModel{}
			al, msgBus := messagingLoop(t, cfg, model)

			alice, _ := al.GetRegistry().Get("alice")
			if _, has := alice.Tools.Get("shell_exec"); has != tc.allowed {
				t.Errorf("Alice has shell_exec = %v, want %v", has, tc.allowed)
			}
			msg := inbound("chat-1", "m1", "run it")
			msg.Channel = "telegram"
			msg.Metadata = map[string]string{metadataKeyPreresolvedAgentID: "alice"}
			dispatch(al, msg)
			checkShell(t, "Alice on telegram", nextOutbound(t, msgBus).Content, "Alice", tc.allowed)
		})
	}
}

// TestShell_ReloadTurnsItOff: a reload that takes shell_exec away from Alice
// applies to her next call and to her existing clone once it is rebuilt.
func TestShell_ReloadTurnsItOff(t *testing.T) {
	t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))
	cfg := shellConfig(t)
	model := &shellModel{}
	al, _ := messagingLoop(t, cfg, model)
	ctx := context.Background()

	cloneID, err := newAgentServices(al, "helper").CreateClone("alice")
	if err != nil {
		t.Fatalf("clone alice: %v", err)
	}
	ask := func(target string) string {
		t.Helper()
		reply, err := al.Ask(ctx, "Helper", target, "run it", 5*time.Second)
		if err != nil {
			t.Fatalf("Ask %s: %v", target, err)
		}
		return reply.Text
	}
	checkShell(t, "Alice before the reload", ask("alice"), "Alice", true)
	checkShell(t, "Alice's clone before the reload", ask(cloneID), "Alice", true)

	next := shellConfig(t)
	next.Agents.BaseDir = cfg.Agents.BaseDir
	next.Agents.List[0].Tools = []string{"file_*"}
	before, _ := al.GetRegistry().Get(cloneID)
	if err := al.ReloadProviderAndConfig(ctx, model, next); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if after, ok := al.GetRegistry().Get(cloneID); !ok || after == before {
		t.Fatal("the reload did not rebuild the clone")
	}
	for _, id := range []string{"alice", cloneID} {
		inst, _ := al.GetRegistry().Get(id)
		inst.Provider = model
	}
	checkShell(t, "Alice after the reload", ask("alice"), "Alice", false)
	checkShell(t, "Alice's clone after the reload", ask(cloneID), "Alice", false)
}

// TestShell_DelegationThroughAllowAgents: Bob has no shell_exec, but an
// agent_spawn targeting Alice runs as Alice (her clone), so Bob reaches her
// shell when his subagents.allow_agents covers her, and is refused the spawn
// when it does not.
func TestShell_DelegationThroughAllowAgents(t *testing.T) {
	for _, tc := range []struct {
		name  string
		allow []string
	}{
		{"allow_agents covers alice", []string{"alice"}},
		{"allow_agents empty", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))
			cfg := shellConfig(t)
			cfg.Tools.Overrides["agent_spawn"] = true
			cfg.Agents.List[1].Tools = []string{"file_*", "agent_spawn"}
			cfg.Agents.List[1].Subagents = &config.SubagentsConfig{AllowAgents: tc.allow}
			model := &shellModel{}
			al, _ := messagingLoop(t, cfg, model)

			bob, _ := al.GetRegistry().Get("bob")
			if _, has := bob.Tools.Get("shell_exec"); has {
				t.Fatal("Bob has shell_exec: the check proves nothing")
			}
			res := bob.Tools.ExecuteWithContext(context.Background(), "agent_spawn",
				map[string]any{"task": "run it", "mode": "wait", "agent_id": "alice"},
				"telegram", "chat-bob", nil)
			if tc.allow == nil {
				if !res.IsError || !strings.Contains(res.ForLLM, "not allowed to spawn agent 'alice'") {
					t.Fatalf("spawn of alice: %+v, want refused", res)
				}
				if got := model.take(); len(got) != 0 {
					t.Fatalf("shell_exec ran %q, want no call", got)
				}
				return
			}
			if res.IsError {
				t.Fatalf("spawn of alice: %s", res.ForLLM)
			}
			checkOneResult(t, "Bob's spawn of alice", model, "Alice", true)
		})
	}
}
