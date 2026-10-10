// ClawEh
// License: MIT

package agent

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/providers"
	"github.com/PivotLLM/ClawEh/tools"
	toolsagents "github.com/PivotLLM/ClawEh/tools/agents"
)

// Whether an agent may run shell commands is its own tool permission for
// shell_exec and nothing else: it holds on every channel and for everything
// acting as the agent. An agent without it does not have the tool at all: it
// is never offered to the model, and a call gets the registry's ordinary
// not-found result. Every test below has an allowed case (Alice) and a
// denied case (Bob).

const (
	shellArgs   = `{"command":"echo shell-ok"}`
	shellOutput = "shell-ok"
)

// noShell is the result of a shell_exec call by an agent that does not have
// the tool: the same as for any tool it does not have.
const noShell = `tool "shell_exec" not found`

// shellModel calls shell_exec on every turn, records the result it gets
// back and whether shell_exec was among the tools it was offered, and
// replies "done: <result>".
type shellModel struct {
	mu      sync.Mutex
	results []string
	offered []bool
}

func (m *shellModel) GetDefaultModel() string { return "mock-model" }

func (m *shellModel) Chat(_ context.Context, messages []providers.Message, defs []providers.ToolDefinition, _ string, _ map[string]any) (*providers.LLMResponse, error) {
	m.mu.Lock()
	m.offered = append(m.offered, slices.ContainsFunc(defs, func(d providers.ToolDefinition) bool {
		return d.Function.Name == config.ShellExecTool
	}))
	m.mu.Unlock()
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
	m.results, m.offered = nil, nil
	return out
}

// checkOffered fails the test unless every model call since the last take
// was offered shell_exec exactly when allowed.
func (m *shellModel) checkOffered(t *testing.T, what string, allowed bool) {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.offered) == 0 {
		t.Fatalf("%s: the model was never called", what)
	}
	for i, got := range m.offered {
		if got != allowed {
			t.Errorf("%s: model call %d offered shell_exec = %v, want %v", what, i+1, got, allowed)
		}
	}
}

// shellConfig is messagingConfig with shell_exec named in Alice's tools
// ("Allow shell commands") and Bob on "*" alone, which does not include it.
// No install-wide setting is involved. Helper may target everyone and has
// agent_message.
func shellConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := messagingConfig(t)
	cfg.Agents.List[0].Tools = []string{"*", "shell_exec"}
	cfg.Agents.List[1].Tools = []string{"*"}
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

// checkShell fails the test unless got is the outcome of one shell_exec call:
// its output when allowed, else exactly the not-found result. A "done: "
// reply prefix is ignored.
func checkShell(t *testing.T, what, got string, allowed bool) {
	t.Helper()
	got = strings.TrimPrefix(got, "done: ")
	if allowed {
		if !strings.Contains(got, shellOutput) || strings.Contains(got, "not found") {
			t.Errorf("%s: %q, want the shell output %q", what, got, shellOutput)
		}
		return
	}
	if got != noShell {
		t.Errorf("%s: %q, want exactly %q", what, got, noShell)
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

// checkOneResult checks the single shell_exec result model recorded, and
// that shell_exec was offered to the model exactly when allowed.
func checkOneResult(t *testing.T, what string, model *shellModel, allowed bool) {
	t.Helper()
	model.checkOffered(t, what, allowed)
	got := model.take()
	if len(got) != 1 {
		t.Fatalf("%s: %d shell_exec results %q, want one", what, len(got), got)
	}
	checkShell(t, what, got[0], allowed)
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
				msg.Metadata[bus.MetaOriginChannel] = "telegram"
			default:
				msg.Channel = kind
			}
			dispatch(al, msg)
			out := nextOutboundTo(t, msgBus, "chat-"+a.id)
			checkShell(t, a.id+" on "+kind, out.Content, a.allowed)
			model.checkOffered(t, a.id+" on "+kind, a.allowed)
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
		checkShell(t, a.id+" re-entry", out.Content, a.allowed)
		model.checkOffered(t, a.id+" re-entry", a.allowed)
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
		checkOneResult(t, a.id+" agent_spawn wait", model, a.allowed)

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
		checkOneResult(t, a.id+" agent_spawn background", model, a.allowed)

		sr, err := toolsagents.NewSpawner(inst.spawnMgr).RunSync(ctx, "run it", "")
		if err != nil {
			t.Fatalf("%s Maestro dispatch: %v", a.id, err)
		}
		checkShell(t, a.id+" Maestro dispatch", sr.Content, a.allowed)
		model.checkOffered(t, a.id+" Maestro dispatch", a.allowed)
		model.take()

		cloneID, err := newAgentServices(al, "helper").CreateClone(a.id)
		if err != nil {
			t.Fatalf("clone %s: %v", a.id, err)
		}
		reply, err := al.Ask(ctx, "Helper", cloneID, "run it", 5*time.Second)
		if err != nil {
			t.Fatalf("ask the clone of %s: %v", a.id, err)
		}
		checkShell(t, a.id+" clone (AgentServices)", reply.Text, a.allowed)
		model.checkOffered(t, a.id+" clone (AgentServices)", a.allowed)
		model.take()

		tr := callMessageTool(t, al, "helper", a.id, float64(5))
		if tr.IsError {
			t.Fatalf("agent_message to %s: %s", a.id, tr.ForLLM)
		}
		checkOneResult(t, a.id+" agent_message", model, a.allowed)

		reply, err = al.Ask(ctx, "Helper", a.id, "run it", 5*time.Second)
		if err != nil {
			t.Fatalf("Ask %s: %v", a.id, err)
		}
		if reply.Outcome != bus.OutcomeOK {
			t.Fatalf("Ask %s: %+v", a.id, reply)
		}
		checkShell(t, a.id+" Ask", reply.Text, a.allowed)
		model.checkOffered(t, a.id+" Ask", a.allowed)
		model.take()
	}
}

// TestShell_FreshAgentNever: a fresh temporary agent has no tools: asked to
// run a command, it is not offered shell_exec and its call is not found,
// even when its creator may run them.
func TestShell_FreshAgentNever(t *testing.T) {
	t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))
	cfg := forumConfig(t, true, false)
	cfg.Agents.List[0].Tools = []string{"*", "shell_exec"}
	model := &shellModel{}
	r := newForumRig(t, cfg, model)
	id, err := newAgentServices(r.al, "alice").CreateFresh("alpha", tools.WithName("Bob"))
	if err != nil {
		t.Fatalf("CreateFresh: %v", err)
	}
	reply, err := r.al.Ask(context.Background(), "Alice", id, "run it", 5*time.Second)
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	checkShell(t, "fresh agent", reply.Text, false)
	checkOneResult(t, "fresh agent", model, false)
}

// TestShell_HumanAgentNever: an agent that stands for a person has no tools,
// so it can never run shell commands, whatever its tool list says.
func TestShell_HumanAgentNever(t *testing.T) {
	t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))
	cfg := humanMessagingConfig(t, 60)
	for i := range cfg.Agents.List {
		cfg.Agents.List[i].Tools = []string{"*", "shell_exec"}
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
		checkShell(t, "human agent", res.ForLLM, false)
	}
}

// TestShell_ConfigSwitches: only shell_exec named in the agent's own tools
// grants it ("*" and prefixes do not), deny_tools wins, and
// tools.tool_overrides.shell_exec, whatever its value, changes nothing.
func TestShell_ConfigSwitches(t *testing.T) {
	alice := func(tools ...string) func(*config.Config) {
		return func(c *config.Config) { c.Agents.List[0].Tools = tools }
	}
	override := func(v bool, tools ...string) func(*config.Config) {
		return func(c *config.Config) {
			c.Tools.Overrides["shell_exec"] = v
			c.Agents.List[0].Tools = tools
		}
	}
	for _, tc := range []struct {
		name    string
		edit    func(cfg *config.Config)
		allowed bool
	}{
		{"tools * and shell_exec", func(*config.Config) {}, true},
		{"tools lists only shell_exec", alice("shell_exec"), true},
		{"tools *", alice("*"), false},
		{"tools shell_*", alice("shell_*"), false},
		{"tools leave it out", alice("file_*"), false},
		{"deny_tools", func(c *config.Config) { c.Agents.List[0].DenyTools = []string{"shell_exec"} }, false},
		{"tool_overrides true, tools *", override(true, "*"), false},
		{"tool_overrides true, named", override(true, "*", "shell_exec"), true},
		{"tool_overrides false, named", override(false, "*", "shell_exec"), true},
		{"tool_overrides false, tools *", override(false, "*"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))
			cfg := shellConfig(t)
			tc.edit(cfg)
			model := &shellModel{}
			al, msgBus := messagingLoop(t, cfg, model)

			inst, _ := al.GetRegistry().Get("alice")
			if _, has := inst.Tools.Get("shell_exec"); has != tc.allowed {
				t.Errorf("Alice has shell_exec = %v, want %v", has, tc.allowed)
			}
			// The MCP host path (session and service tokens) runs the same
			// registry.
			res := inst.Tools.ExecuteForHost(context.Background(), "shell_exec",
				map[string]any{"command": "echo shell-ok"}, "telegram", "chat-1", nil)
			checkShell(t, "Alice through the host", res.ForLLM, tc.allowed)
			msg := inbound("chat-1", "m1", "run it")
			msg.Channel = "telegram"
			msg.Metadata = map[string]string{metadataKeyPreresolvedAgentID: "alice"}
			dispatch(al, msg)
			checkShell(t, "Alice on telegram", nextOutbound(t, msgBus).Content, tc.allowed)
			model.checkOffered(t, "Alice on telegram", tc.allowed)
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
	checkShell(t, "Alice before the reload", ask("alice"), true)
	checkShell(t, "Alice's clone before the reload", ask(cloneID), true)

	next := shellConfig(t)
	next.Agents.BaseDir = cfg.Agents.BaseDir
	next.Agents.List[0].Tools = []string{"*"}
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
	model.take()
	checkShell(t, "Alice after the reload", ask("alice"), false)
	checkShell(t, "Alice's clone after the reload", ask(cloneID), false)
	model.checkOffered(t, "Alice and her clone after the reload", false)
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
			cfg.Agents.List[1].Tools = []string{"*"}
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
			checkOneResult(t, "Bob's spawn of alice", model, true)
		})
	}
}
