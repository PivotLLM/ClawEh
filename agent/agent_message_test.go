// ClawEh
// License: MIT

package agent

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/agentreg"
	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/constants"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/providers"
	"github.com/PivotLLM/ClawEh/tools"
	toolsagents "github.com/PivotLLM/ClawEh/tools/agents"
)

// chatFunc is a model whose answer a test scripts.
type chatFunc func(ctx context.Context, messages []providers.Message) (*providers.LLMResponse, error)

func (f chatFunc) Chat(ctx context.Context, messages []providers.Message, _ []providers.ToolDefinition, _ string, _ map[string]any) (*providers.LLMResponse, error) {
	return f(ctx, messages)
}

func (f chatFunc) GetDefaultModel() string { return "mock-model" }

// lastUser is the content of the last user message sent to the model.
func lastUser(messages []providers.Message) string {
	for _, m := range slices.Backward(messages) {
		if m.Role == "user" {
			return m.Content
		}
	}
	return ""
}

// messagingConfig has Alice (the default agent, allowed to target Bob), Bob
// (bound to the telegram user u1, allowed nothing) and Helper (allowed "*"),
// with agent_message switched on.
func messagingConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := newTestConfig(t)
	cfg.Tools.Subagent.Enabled = true
	cfg.Tools.Overrides = map[string]bool{"agent_message": true}
	cfg.Agents.List = []config.AgentConfig{
		{ID: "alice", Name: "Alice", Default: true, Subagents: &config.SubagentsConfig{AllowAgents: []string{"bob"}}},
		{ID: "bob", Name: "Bob"},
		{ID: "helper", Name: "Helper", Subagents: &config.SubagentsConfig{AllowAgents: []string{"*"}}},
	}
	cfg.Bindings = []config.AgentBinding{{
		AgentID: "bob",
		Match:   config.BindingMatch{Channel: "telegram", AccountID: "*", Peer: &config.PeerMatch{Kind: "direct", ID: "u1"}},
	}}
	return cfg
}

// messagingLoop builds a running loop over cfg whose agents answer with
// model: inbound messages on the bus are dispatched as Run does.
func messagingLoop(t *testing.T, cfg *config.Config, model providers.LLMProvider) (*AgentLoop, *bus.MessageBus) {
	t.Helper()
	tools.RegisterProvider(tools.NamespacedProvider("agent", toolsagents.GlobalProvider))
	msgBus := bus.NewMessageBus()
	al := mustNewAgentLoop(t, cfg, msgBus, model, nil)
	for _, id := range al.GetRegistry().List() {
		if a, ok := al.GetRegistry().Get(id); ok {
			a.Provider = model
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	al.running.Store(true)
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			msg, ok := msgBus.ConsumeInbound(ctx)
			if !ok {
				return
			}
			al.activeRequests.Add(1)
			go al.processSessionMessage(ctx, msg)
		}
	})
	t.Cleanup(func() {
		cancel()
		wg.Wait()
		// Turns still running (a late reply, a background result) finish
		// before the test's directories and logger go away.
		al.activeRequests.Wait()
	})
	return al, msgBus
}

// probeTool records the context of each call.
type probeTool struct {
	mu      sync.Mutex
	depths  []int
	channel []string
	chains  [][]string
	remote  []bool
}

func (p *probeTool) Name() string               { return "probe_tool" }
func (p *probeTool) Description() string        { return "test probe" }
func (p *probeTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (p *probeTool) Execute(ctx context.Context, _ map[string]any) *tools.ToolResult {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.depths = append(p.depths, toolsagents.SpawnDepth(ctx))
	p.channel = append(p.channel, tools.ToolChannel(ctx))
	p.chains = append(p.chains, tools.AskChain(ctx))
	p.remote = append(p.remote, tools.RemoteOrigin(ctx))
	return tools.NewToolResult("probe ok")
}

// toolThenText calls probe_tool once, then replies with the tool's result.
func toolThenText() chatFunc {
	return callThenText("probe_tool", "{}")
}

// callThenText calls tool with args once, then replies with its result.
func callThenText(tool, args string) chatFunc {
	return func(_ context.Context, messages []providers.Message) (*providers.LLMResponse, error) {
		last := messages[len(messages)-1]
		if last.Role == "tool" {
			return &providers.LLMResponse{Content: "done: " + last.Content}, nil
		}
		return &providers.LLMResponse{ToolCalls: []providers.ToolCall{{
			ID: "tc-1", Type: "function", Name: tool,
			Function: &providers.FunctionCall{Name: tool, Arguments: args},
		}}}, nil
	}
}

// setModel gives agent id its own model.
func setModel(t *testing.T, al *AgentLoop, id string, model providers.LLMProvider) {
	t.Helper()
	a, ok := al.GetRegistry().Get(id)
	if !ok {
		t.Fatalf("no agent %s", id)
	}
	a.Provider = model
}

// TestAsk_ReturnsReplyAndOutcome: the target gets a normal turn headed with
// the sender's name, and Ask returns its final reply with the turn's outcome.
// Nothing is published to any channel.
func TestAsk_ReturnsReplyAndOutcome(t *testing.T) {
	t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))

	for _, tc := range []struct {
		name        string
		resp        *providers.LLMResponse
		err         error
		wantOutcome string
		wantText    string
	}{
		{name: "ok", resp: &providers.LLMResponse{Content: "pong"}, wantOutcome: bus.OutcomeOK, wantText: "pong"},
		{name: "empty", resp: &providers.LLMResponse{FinishReason: "stop", Normal: true}, wantOutcome: bus.OutcomeEmpty},
		{name: "error", err: errors.New("boom"), wantOutcome: bus.OutcomeError, wantText: "boom"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var seen string
			model := chatFunc(func(_ context.Context, messages []providers.Message) (*providers.LLMResponse, error) {
				seen = lastUser(messages)
				if tc.err != nil {
					return nil, tc.err
				}
				r := *tc.resp
				return &r, nil
			})
			al, msgBus := messagingLoop(t, messagingConfig(t), model)

			reply, err := al.Ask(context.Background(), "Alice", "bob", "ping", 5*time.Second)
			if err != nil {
				t.Fatalf("Ask: %v", err)
			}
			if reply.Outcome != tc.wantOutcome || !strings.Contains(reply.Text, tc.wantText) {
				t.Fatalf("reply = %+v, want outcome %q text containing %q", reply, tc.wantOutcome, tc.wantText)
			}
			if tc.wantText == "" && reply.Text != "" {
				t.Fatalf("empty reply carried text %q", reply.Text)
			}
			if want := "[Message from Alice — Alice is waiting for your reply]\nping"; !strings.HasSuffix(seen, want) {
				t.Fatalf("Bob was sent %q, want it to end with %q", seen, want)
			}
			noOutbound(t, msgBus)
		})
	}
}

// TestAsk_TurnRunsTools: the asked turn is a full agent turn: its tool calls
// run, on the ask channel, one sub-agent level deeper than the asker, with
// the asker in the ask chain.
func TestAsk_TurnRunsTools(t *testing.T) {
	t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))

	al, msgBus := messagingLoop(t, messagingConfig(t), toolThenText())
	probe := &probeTool{}
	al.RegisterTool(probe)

	ctx := tools.WithAskChain(toolsagents.WithSpawnDepth(context.Background(), 1), []string{"alice"})
	reply, err := al.Ask(ctx, "Alice", "bob", "use the probe", 5*time.Second)
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if reply.Outcome != bus.OutcomeOK || reply.Text != "done: probe ok" {
		t.Fatalf("reply = %+v, want ok %q", reply, "done: probe ok")
	}
	probe.mu.Lock()
	defer probe.mu.Unlock()
	if len(probe.depths) != 1 || probe.depths[0] != 2 {
		t.Fatalf("tool ran at depths %v, want one call at depth 2", probe.depths)
	}
	if probe.channel[0] != constants.AgentMessageChannel {
		t.Fatalf("tool channel = %q, want %q", probe.channel[0], constants.AgentMessageChannel)
	}
	if got := strings.Join(probe.chains[0], ","); got != "alice,bob" {
		t.Fatalf("ask chain in Bob's turn = %q, want alice,bob", got)
	}
	if probe.remote[0] {
		t.Fatal("an ask from a local turn ran remote")
	}
	noOutbound(t, msgBus)
}

// TestRemoteOrigin_Hops: a turn on a remote chat runs remote and an ask from
// it hands the mark on; a turn on an internal channel runs local.
func TestRemoteOrigin_Hops(t *testing.T) {
	t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))

	al, msgBus := messagingLoop(t, messagingConfig(t), toolThenText())
	probe := &probeTool{}
	al.RegisterTool(probe)

	tg := inbound("c1", "m1", "go")
	tg.Channel = "telegram"
	dispatch(al, tg)
	nextOutbound(t, msgBus)
	local := inbound("c2", "m2", "go")
	local.Channel = "cli"
	dispatch(al, local)
	nextOutbound(t, msgBus)
	if _, err := al.Ask(tools.WithRemoteOrigin(context.Background()), "Alice", "bob", "go", 5*time.Second); err != nil {
		t.Fatalf("Ask: %v", err)
	}

	probe.mu.Lock()
	defer probe.mu.Unlock()
	if want := []bool{true, false, true}; !slices.Equal(probe.remote, want) {
		t.Fatalf("remote marks (telegram turn, cli turn, ask from remote) = %v, want %v", probe.remote, want)
	}
}

// asyncTool finishes in the background, then reports "background done".
type asyncTool struct{}

func (asyncTool) Name() string               { return "async_tool" }
func (asyncTool) Description() string        { return "test background work" }
func (asyncTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (asyncTool) Execute(context.Context, map[string]any) *tools.ToolResult {
	return tools.ErrorResult("async only")
}

func (asyncTool) ExecuteAsync(ctx context.Context, _ map[string]any, cb tools.AsyncCallback) *tools.ToolResult {
	go func() {
		time.Sleep(50 * time.Millisecond)
		cb(ctx, &tools.ToolResult{ForLLM: "background done"})
	}()
	return tools.AsyncResult("started")
}

// TestAsk_BackgroundResultReachesMainConversation: background work an asked
// turn starts reports to the asked agent's own main conversation once the ask
// is over, and nothing reaches a chat.
func TestAsk_BackgroundResultReachesMainConversation(t *testing.T) {
	t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))

	// Bob has a default chat: the result must not go there either.
	cfg := messagingConfig(t)
	cfg.Bindings[0].Default = true
	var al *AgentLoop
	results := make(chan string, 1)
	pending := make(chan bool, 1)
	model := chatFunc(func(ctx context.Context, messages []providers.Message) (*providers.LLMResponse, error) {
		if u := lastUser(messages); strings.Contains(u, "[System: async:async_tool]") {
			_, rec := al.agentStates["bob"].GetPendingTurn("agent:bob:main")
			pending <- rec
			results <- u
			return &providers.LLMResponse{Content: "noted"}, nil
		}
		return callThenText("async_tool", "{}")(ctx, messages)
	})
	al, msgBus := messagingLoop(t, cfg, model)
	al.RegisterTool(asyncTool{})
	sti := &scopeSTI{}
	sti.SetSource("agent:bob:main", "telegram", "chat-1")
	al.SetSessionTokenIssuer(sti)

	reply, err := al.Ask(context.Background(), "Alice", "bob", "start it", 5*time.Second)
	if err != nil || reply.Outcome != bus.OutcomeOK {
		t.Fatalf("Ask = %+v, %v", reply, err)
	}
	select {
	case u := <-results:
		if !strings.Contains(u, "background done") {
			t.Fatalf("result message = %q", u)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the background result never reached Bob")
	}
	if <-pending {
		t.Fatal("the background result's turn kept a recovery record")
	}
	bob, _ := al.GetRegistry().Get("bob")
	deadline := time.Now().Add(5 * time.Second)
	for {
		found := false
		for _, m := range bob.Sessions.GetHistory("agent:bob:main") {
			if strings.Contains(m.Content, "background done") {
				found = true
			}
		}
		if found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the background result is not in Bob's main conversation")
		}
		time.Sleep(20 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	for {
		out, ok := msgBus.SubscribeOutbound(ctx)
		if !ok {
			break
		}
		t.Fatalf("published: %+v", out)
	}
	for _, c := range sti.calls() {
		if c.sessionKey == "agent:bob:main" && !constants.IsInternalChannel(c.channel) && (c.channel != "telegram" || c.chatID != "chat-1") {
			t.Fatalf("the session source was moved to a chat: %+v", c)
		}
	}
	if ch, chat := sti.Source("agent:bob:main"); ch != "telegram" || chat != "chat-1" {
		t.Fatalf("source after = %s/%s, want telegram/chat-1 unchanged", ch, chat)
	}
}

// TestAsk_SlotLentWhileWaiting: with max_concurrent_turns 1, Alice's turn
// lends its slot while it waits on Bob, so a message queued for Bob ahead of
// the ask can run, and the ask completes.
func TestAsk_SlotLentWhileWaiting(t *testing.T) {
	t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))

	cfg := messagingConfig(t)
	cfg.Agents.Defaults.MaxConcurrentTurns = 1
	al, msgBus := messagingLoop(t, cfg, &recordingProvider{})
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	ask := callThenText("agent_message", `{"agent":"bob","message":"hi","wait_seconds":10}`)
	setModel(t, al, "alice", chatFunc(func(ctx context.Context, messages []providers.Message) (*providers.LLMResponse, error) {
		if messages[len(messages)-1].Role != "tool" {
			started <- struct{}{}
			<-release
		}
		return ask(ctx, messages)
	}))
	setModel(t, al, "bob", chatFunc(func(_ context.Context, messages []providers.Message) (*providers.LLMResponse, error) {
		return &providers.LLMResponse{Content: "bob: " + lastUser(messages)[:4]}, nil
	}))

	msgBus.PublishInbound(context.Background(), inbound("c1", "m1", "ask bob")) //nolint:errcheck // test
	<-started
	// Queued for Bob while Alice holds the only slot.
	forBob := inbound("c2", "m2", "plain")
	forBob.Metadata = map[string]string{metadataKeyPreresolvedAgentID: "bob"}
	msgBus.PublishInbound(context.Background(), forBob) //nolint:errcheck // test
	time.Sleep(100 * time.Millisecond)
	close(release)

	got := map[string]string{}
	for range 2 {
		out := nextOutbound(t, msgBus)
		got[out.ChatID] = out.Content
	}
	if !strings.HasPrefix(got["c1"], "done: bob: ") || got["c2"] == "" {
		t.Fatalf("replies = %v, want Bob's plain reply and Alice relaying Bob's answer", got)
	}
}

// TestAsk_RefusesCrossExchangeCycle: while Alice waits on Bob, Bob (in
// another turn) cannot ask Alice; Bob can still ask an agent not waiting on
// him.
func TestAsk_RefusesCrossExchangeCycle(t *testing.T) {
	t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))

	model := newGatedRecorder()
	al, _ := messagingLoop(t, messagingConfig(t), model)
	done := make(chan error, 1)
	go func() {
		_, err := al.Ask(tools.WithAskChain(context.Background(), []string{"alice"}), "Alice", "bob", "hi", 5*time.Second)
		done <- err
	}()
	model.waitStarted(t)

	bobsTurn := tools.WithAskChain(context.Background(), []string{"bob"})
	if _, err := al.Ask(bobsTurn, "Bob", "alice", "hi", time.Second); !errors.Is(err, tools.ErrAskLoop) {
		t.Fatalf("Bob asking Alice while she waits on him: err = %v, want ErrAskLoop", err)
	}
	close(model.gate)
	if err := <-done; err != nil {
		t.Fatalf("Alice's ask: %v", err)
	}
	// The wait is over: Bob may ask Alice now.
	if reply, err := al.Ask(bobsTurn, "Bob", "alice", "hi", 5*time.Second); err != nil || reply.Outcome != bus.OutcomeOK {
		t.Fatalf("Bob asking Alice afterwards = %+v, %v", reply, err)
	}
}

// TestAsk_StaleAskNotRun: an ask whose asker stopped waiting before Bob got
// to it runs no model turn.
func TestAsk_StaleAskNotRun(t *testing.T) {
	t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))

	model := newGatedRecorder()
	al, msgBus := messagingLoop(t, messagingConfig(t), model)
	busy := inbound("c1", "m1", "busy")
	busy.Metadata = map[string]string{metadataKeyPreresolvedAgentID: "bob"}
	msgBus.PublishInbound(context.Background(), busy) //nolint:errcheck // test
	model.waitStarted(t)

	reply, err := al.Ask(context.Background(), "Alice", "bob", "late", 100*time.Millisecond)
	if err != nil || reply.Outcome != tools.OutcomeTimeout {
		t.Fatalf("Ask = %+v, %v; want a timeout", reply, err)
	}
	close(model.gate)
	nextOutbound(t, msgBus)
	time.Sleep(200 * time.Millisecond)
	if n := model.count(); n != 1 {
		t.Fatalf("model calls = %d, want only the busy turn", n)
	}
}

// TestAsk_WithOneTurnSlot: with max_concurrent_turns 1, a turn that asks
// another agent still gets its reply: the asked turn takes no slot.
func TestAsk_WithOneTurnSlot(t *testing.T) {
	t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))

	cfg := messagingConfig(t)
	cfg.Agents.Defaults.MaxConcurrentTurns = 1
	al, msgBus := messagingLoop(t, cfg, &recordingProvider{})
	setModel(t, al, "alice", callThenText("agent_message", `{"agent":"bob","message":"hi","wait_seconds":5}`))
	setModel(t, al, "bob", chatFunc(func(context.Context, []providers.Message) (*providers.LLMResponse, error) {
		return &providers.LLMResponse{Content: "bob says hi"}, nil
	}))

	dispatch(al, inbound("c1", "m1", "ask bob"))
	if out := nextOutbound(t, msgBus); out.Content != "done: bob says hi" {
		t.Fatalf("reply = %q, want Alice to relay Bob's answer", out.Content)
	}
}

// scopeSTI records the turn scope and sources the loop sets on the token.
type scopeSTI struct {
	recordingSTI
	scopeMu sync.Mutex
	scopes  map[string][]string
}

func (s *scopeSTI) SetTurnScope(sessionKey string, chain []string, _ bool) {
	s.scopeMu.Lock()
	defer s.scopeMu.Unlock()
	if s.scopes == nil {
		s.scopes = map[string][]string{}
	}
	s.scopes[sessionKey] = chain
}

// TestAsk_SessionTokenScope: an asked turn points its session token's source
// at the ask while it runs and restores the chat it had after, and records
// the ask chain on the token for a CLI agent's MCP calls.
func TestAsk_SessionTokenScope(t *testing.T) {
	t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))

	al, _ := messagingLoop(t, messagingConfig(t), &recordingProvider{})
	sti := &scopeSTI{}
	sti.SetSource("agent:bob:main", "telegram", "chat-1")
	al.SetSessionTokenIssuer(sti)

	if _, err := al.Ask(tools.WithAskChain(context.Background(), []string{"alice"}), "Alice", "bob", "hi", 5*time.Second); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	var bobSources []sourceCall
	for _, c := range sti.calls() {
		if c.sessionKey == "agent:bob:main" {
			bobSources = append(bobSources, c)
		}
	}
	if len(bobSources) != 3 || bobSources[1].channel != constants.AgentMessageChannel ||
		bobSources[2].channel != "telegram" || bobSources[2].chatID != "chat-1" {
		t.Fatalf("bob's sources = %+v, want telegram, the ask, then telegram again", bobSources)
	}
	sti.scopeMu.Lock()
	defer sti.scopeMu.Unlock()
	if got := sti.scopes["agent:bob:main"]; !slices.Equal(got, []string{"alice", "bob"}) {
		t.Fatalf("token ask chain = %v, want [alice bob]", got)
	}
}

// TestAsk_Timeout: an ask the target does not answer in time returns the
// timeout outcome; the turn finishes later and its reply is discarded, not
// published. The target's request_timeout caps a longer wait.
func TestAsk_Timeout(t *testing.T) {
	t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))

	t.Run("wait elapses", func(t *testing.T) {
		model := newGatedRecorder()
		al, msgBus := messagingLoop(t, messagingConfig(t), model)

		reply, err := al.Ask(context.Background(), "Alice", "bob", "slow", 200*time.Millisecond)
		if err != nil {
			t.Fatalf("Ask: %v", err)
		}
		if reply.Outcome != tools.OutcomeTimeout || reply.Text != "Bob did not reply within 1 second." {
			t.Fatalf("reply = %+v, want the timeout outcome", reply)
		}
		model.waitStarted(t)
		close(model.gate)
		noOutbound(t, msgBus)
	})

	t.Run("request_timeout caps the wait", func(t *testing.T) {
		cfg := messagingConfig(t)
		cfg.Agents.Defaults.RequestTimeout = 1
		model := newGatedRecorder()
		al, _ := messagingLoop(t, cfg, model)
		defer close(model.gate)

		start := time.Now()
		reply, err := al.Ask(context.Background(), "Alice", "bob", "slow", time.Hour)
		if err != nil {
			t.Fatalf("Ask: %v", err)
		}
		if reply.Outcome != tools.OutcomeTimeout || time.Since(start) > 10*time.Second {
			t.Fatalf("reply = %+v after %v, want a timeout after about 1s", reply, time.Since(start))
		}
	})
}

// TestAsk_Refusals: an ask is refused at the depth limit, to an agent already
// waiting in the exchange, and to an unknown agent; no turn starts.
func TestAsk_Refusals(t *testing.T) {
	t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))

	model := &recordingProvider{}
	al, _ := messagingLoop(t, messagingConfig(t), model)
	limit := config.DefaultMaxSubagentDepth

	for _, tc := range []struct {
		name  string
		ctx   context.Context
		agent string
		want  error
	}{
		{"at the depth limit", toolsagents.WithSpawnDepth(context.Background(), limit), "bob", tools.ErrMaxDepth},
		{"target waiting in the exchange", tools.WithAskChain(context.Background(), []string{"bob", "alice"}), "bob", tools.ErrAskLoop},
		{"unknown agent", context.Background(), "zed", tools.ErrNoSuchAgent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := al.Ask(tc.ctx, "Alice", tc.agent, "hi", time.Second)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}

	// No time left: a timeout at once, and no turn.
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if reply, err := al.Ask(expired, "Alice", "bob", "hi", time.Second); err != nil || reply.Outcome != tools.OutcomeTimeout {
		t.Fatalf("ask with no time left = %+v, %v; want a timeout", reply, err)
	}

	// One below the limit is allowed.
	reply, err := al.Ask(toolsagents.WithSpawnDepth(context.Background(), limit-1), "Alice", "bob", "hi", 5*time.Second)
	if err != nil || reply.Outcome != bus.OutcomeOK {
		t.Fatalf("ask below the limit = %+v, %v; want ok", reply, err)
	}
	if model.count() != 1 {
		t.Fatalf("model calls = %d, want only the allowed ask", model.count())
	}
}

// TestWhisper_DeliveredOnceAtStartOfNextMessage: a whisper starts no turn;
// it opens the target's next message, from any source, exactly once. The
// hint on answering is given only to an agent with agent_message.
func TestWhisper_DeliveredOnceAtStartOfNextMessage(t *testing.T) {
	t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))

	for _, tc := range []struct {
		name     string
		denied   bool
		wantHead string
	}{
		{"without the tool", true, "[Private whisper from Alice — no reply expected.] psst\n\n"},
		{"with the tool", false, "[Private whisper from Alice — no reply expected. To answer privately, use agent_message with wait_seconds 0.] psst\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := messagingConfig(t)
			if tc.denied {
				cfg.Agents.List[1].DenyTools = []string{"agent_message"}
			}
			model := &recordingProvider{}
			al, msgBus := messagingLoop(t, cfg, model)

			if err := al.Whisper(context.Background(), "Alice", "bob", "psst"); err != nil {
				t.Fatalf("Whisper: %v", err)
			}
			noOutbound(t, msgBus)
			if model.count() != 0 {
				t.Fatalf("a whisper started a turn (%d model calls)", model.count())
			}

			for i, text := range []string{"first", "second"} {
				msg := inbound("c1", text, text)
				msg.Metadata = map[string]string{metadataKeyPreresolvedAgentID: "bob"}
				dispatch(al, msg)
				nextOutbound(t, msgBus)
				got := lastUser(model.call(i).messages)
				hasWhisper := strings.Contains(got, "psst")
				switch {
				case i == 0 && !strings.HasPrefix(got, tc.wantHead):
					t.Fatalf("first message = %q, want it to start with %q", got, tc.wantHead)
				case i == 1 && hasWhisper:
					t.Fatalf("the whisper was delivered twice: %q", got)
				}
			}
		})
	}
}

// callMessageTool runs callerID's agent_message tool.
func callMessageTool(t *testing.T, al *AgentLoop, callerID, target string, wait any) *tools.ToolResult {
	t.Helper()
	caller, ok := al.GetRegistry().Get(callerID)
	if !ok {
		t.Fatalf("no agent %s", callerID)
	}
	tool, ok := caller.Tools.Get("agent_message")
	if !ok {
		t.Fatalf("%s has no agent_message", callerID)
	}
	return tool.Execute(context.Background(), map[string]any{"agent": target, "message": "hello", "wait_seconds": wait})
}

// TestAgentMessageTool_AllowAgents: agent_message reaches only agents in the
// caller's subagents.allow_agents ("*" for all), whispers with wait_seconds
// 0 and asks otherwise.
func TestAgentMessageTool_AllowAgents(t *testing.T) {
	t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))

	for _, tc := range []struct {
		name, caller, target string
		wait                 any
		wantErr              bool
		want                 string
	}{
		{"listed target, whisper", "alice", "bob", float64(0), false, "Whispered to Bob."},
		{"listed target by name, ask", "alice", "Bob", float64(5), false, "reply 1"},
		{"huge wait is clamped", "alice", "bob", float64(1e300), false, "reply 1"},
		{"unlisted target", "alice", "helper", float64(0), true, "You may not message Helper"},
		{"empty allow list", "bob", "alice", float64(0), true, "You may not message Alice"},
		{"wildcard", "helper", "alice", float64(0), false, "Whispered to Alice."},
		{"unknown agent", "helper", "zed", float64(0), true, "There is no agent named zed."},
		{"missing wait", "alice", "bob", nil, true, "wait_seconds is required"},
		{"negative wait", "alice", "bob", float64(-1), true, "wait_seconds is required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			al, _ := messagingLoop(t, messagingConfig(t), &recordingProvider{})
			res := callMessageTool(t, al, tc.caller, tc.target, tc.wait)
			if res.IsError != tc.wantErr || !strings.Contains(res.ForLLM, tc.want) {
				t.Fatalf("result = %+v, want error=%v containing %q", res, tc.wantErr, tc.want)
			}
		})
	}
}

// TestAgentMessageTool_CloneUsesSourceList: a clone of Alice messages with
// Alice's allow list, as Alice.
func TestAgentMessageTool_CloneUsesSourceList(t *testing.T) {
	t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))

	al, _ := messagingLoop(t, messagingConfig(t), &recordingProvider{})
	cloneID, err := al.GetRegistry().Create(config.AgentConfig{}, agentreg.CloneOf("alice"))
	if err != nil {
		t.Fatalf("clone alice: %v", err)
	}
	if res := callMessageTool(t, al, cloneID, "bob", float64(0)); res.IsError || res.ForLLM != "Whispered to Bob." {
		t.Fatalf("clone of alice -> bob = %+v, want the whisper sent", res)
	}
	if res := callMessageTool(t, al, cloneID, "helper", float64(0)); !res.IsError {
		t.Fatalf("clone of alice -> helper = %+v, want refused", res)
	}
	if ws := al.whispers.take("bob"); len(ws) != 1 || ws[0].from != "Alice" {
		t.Fatalf("bob's whispers = %+v, want one from Alice", ws)
	}
}

// TestWhispers_KeptAcrossRebuildDroppedOnRemoval: a reload that rebuilds a
// temporary agent keeps its held whispers; deleting it, or removing a config
// agent, drops them.
func TestWhispers_KeptAcrossRebuildDroppedOnRemoval(t *testing.T) {
	t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))

	cfg := messagingConfig(t)
	model := &recordingProvider{}
	al, _ := messagingLoop(t, cfg, model)
	reg := al.GetRegistry()
	cloneID, err := reg.Create(config.AgentConfig{}, agentreg.CloneOf("alice"))
	if err != nil {
		t.Fatalf("clone alice: %v", err)
	}
	for _, id := range []string{cloneID, "helper"} {
		if err := al.Whisper(context.Background(), "Alice", id, "psst"); err != nil {
			t.Fatalf("Whisper %s: %v", id, err)
		}
	}

	before, _ := reg.Get(cloneID)
	next := messagingConfig(t)
	next.Agents.BaseDir = cfg.Agents.BaseDir
	next.Agents.List = next.Agents.List[:2] // helper removed
	if err := al.ReloadProviderAndConfig(context.Background(), model, next); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if after, ok := reg.Get(cloneID); !ok || after == before {
		t.Fatal("the reload did not rebuild the clone")
	}
	if ws := al.whispers.take("helper"); len(ws) != 0 {
		t.Fatalf("a removed agent kept its whispers: %+v", ws)
	}
	if ws := al.whispers.take(cloneID); len(ws) != 1 {
		t.Fatalf("the rebuilt clone's whispers = %+v, want one", ws)
	}

	if err := al.Whisper(context.Background(), "Alice", cloneID, "again"); err != nil {
		t.Fatalf("Whisper: %v", err)
	}
	if err := reg.Delete(cloneID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if ws := al.whispers.take(cloneID); len(ws) != 0 {
		t.Fatalf("a deleted agent kept its whispers: %+v", ws)
	}
}

// TestAgentMessageTool_OffByDefault: without the per-tool switch no agent
// has agent_message, whatever its allow list.
func TestAgentMessageTool_OffByDefault(t *testing.T) {
	t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))

	cfg := messagingConfig(t)
	cfg.Tools.Overrides = nil
	al, _ := messagingLoop(t, cfg, &recordingProvider{})
	for _, id := range []string{"alice", "helper"} {
		a, _ := al.GetRegistry().Get(id)
		if _, ok := a.Tools.Get("agent_message"); ok {
			t.Fatalf("%s has agent_message with the tool switched off", id)
		}
	}
}

// TestCommands_AskAndWhisper: /ask and /whisper work for a sender whose chat
// routes to the target, and are refused, by name, for one whose does not.
// /ask posts the target's reply to the chat, attributed to it.
func TestCommands_AskAndWhisper(t *testing.T) {
	t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))

	from := func(user, text string) bus.InboundMessage {
		return bus.InboundMessage{
			Channel: "telegram", ChatID: "chat-" + user, SenderID: user, MessageID: "m-" + text,
			Content: text, Peer: bus.Peer{Kind: "direct", ID: user},
			Sender: bus.SenderInfo{DisplayName: "User One"},
		}
	}
	for _, tc := range []struct {
		name, user, text, want string
	}{
		{"whisper allowed", "u1", "/whisper bob hello there", "Whispered to Bob."},
		{"whisper refused", "u2", "/whisper bob hello", "You don't have permission to /whisper Bob"},
		{"ask refused", "u2", "/ask Bob hello", "You don't have permission to /ask Bob"},
		{"ask allowed", "u1", "/ask Bob hello", "Bob: reply 1"},
		{"default agent allowed", "u2", "/ask alice hello", "Alice: reply 1"},
		{"unknown agent", "u1", "/ask zed hello", "There is no agent named zed."},
		{"usage", "u1", "/ask bob", "Usage: /ask <agent> <message>"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := &recordingProvider{}
			al, msgBus := messagingLoop(t, messagingConfig(t), model)

			dispatch(al, from(tc.user, tc.text))
			out := nextOutbound(t, msgBus)
			if out.Content != tc.want || out.Channel != "telegram" || out.ChatID != "chat-"+tc.user {
				t.Fatalf("reply = %+v, want %q in telegram/chat-%s", out, tc.want, tc.user)
			}
			if strings.HasPrefix(tc.text, "/ask") && strings.Contains(tc.want, ": reply") {
				if got := lastUser(model.call(0).messages); !strings.Contains(got, "[Message from User One (a person, via /ask on telegram) — User One is waiting for your reply]\nhello") {
					t.Fatalf("the asked agent was sent %q", got)
				}
			}
			noOutbound(t, msgBus)
		})
	}
}

// TestCommands_WhisperMarksAPerson: a whisper sent with /whisper tells the
// agent it is from a person, and how it came.
func TestCommands_WhisperMarksAPerson(t *testing.T) {
	t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))

	model := &recordingProvider{}
	al, msgBus := messagingLoop(t, messagingConfig(t), model)
	dispatch(al, bus.InboundMessage{
		Channel: "telegram", ChatID: "chat-u1", SenderID: "u1", Content: "/whisper bob psst",
		Peer: bus.Peer{Kind: "direct", ID: "u1"}, Sender: bus.SenderInfo{DisplayName: "Alice"},
	})
	nextOutbound(t, msgBus)

	msg := inbound("c1", "m1", "next")
	msg.Metadata = map[string]string{metadataKeyPreresolvedAgentID: "bob"}
	dispatch(al, msg)
	nextOutbound(t, msgBus)
	want := "[Private whisper from Alice (a person, via /whisper on telegram) — no reply expected."
	if got := lastUser(model.call(0).messages); !strings.HasPrefix(got, want) {
		t.Fatalf("Bob's next message = %q, want it to start with %q", got, want)
	}
}

// TestCommands_AskStopsWithTheService: a /ask still waiting when the service
// stops is abandoned without posting anything.
func TestCommands_AskStopsWithTheService(t *testing.T) {
	t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))

	model := newGatedRecorder()
	al, msgBus := messagingLoop(t, messagingConfig(t), model)
	runCtx, stop := context.WithCancel(context.Background())
	al.runMu.Lock()
	al.runCtx = runCtx
	al.runMu.Unlock()
	defer close(model.gate)

	dispatch(al, bus.InboundMessage{
		Channel: "telegram", ChatID: "chat-u1", SenderID: "u1", Content: "/ask bob hi",
		Peer: bus.Peer{Kind: "direct", ID: "u1"},
	})
	model.waitStarted(t)
	stop()
	noOutbound(t, msgBus)
}

// TestAsk_CancelledBeforeStart: an ask dropped by /cancel before its turn
// ran gets the cancelled outcome, still not on any channel.
func TestAsk_CancelledBeforeStart(t *testing.T) {
	t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))

	al := &AgentLoop{}
	replies := al.asks.open("ask-1")
	al.publishCancelledReplies(context.Background(), []bus.InboundMessage{{
		Channel: constants.AgentMessageChannel, ChatID: "ask-1",
		Metadata: map[string]string{bus.MetaReplyRequired: "1"},
	}})
	select {
	case r := <-replies:
		if r.Outcome != bus.OutcomeCancelled {
			t.Fatalf("reply = %+v, want cancelled", r)
		}
	default:
		t.Fatal("no reply delivered")
	}
}

// TestRemoteOrigin_AsyncReentry: a background result of remote-origin work
// re-enters marked remote (and an internal message without the mark stays
// local); a local one stays local.
func TestRemoteOrigin_AsyncReentry(t *testing.T) {
	t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))

	for _, remote := range []bool{false, true} {
		msgBus := bus.NewMessageBus()
		al := mustNewAgentLoop(t, newTestConfig(t), msgBus, &mockProvider{}, nil)
		al.taskPointerCallback("subagent", "x", "main", 1, remote)(context.Background(), &tools.ToolResult{ForLLM: "done"})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		msg, ok := msgBus.ConsumeInbound(ctx)
		cancel()
		if !ok {
			t.Fatal("no re-entry published")
		}
		if got := tools.RemoteOrigin(withInboundOrigin(context.Background(), msg)); got != remote {
			t.Fatalf("re-entered turn remote = %v, want %v", got, remote)
		}
	}
}
