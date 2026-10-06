// ClawEh
// License: MIT

package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/PivotLLM/ClawEh/agentreg"
	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/channels"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/constants"
	"github.com/PivotLLM/ClawEh/logger"
	climcp "github.com/PivotLLM/ClawEh/mcp"
	"github.com/PivotLLM/ClawEh/providers"
	"github.com/PivotLLM/ClawEh/routing"
	"github.com/PivotLLM/ClawEh/tools"
)

// countingProvider answers for the ordinary agent and counts every call, so
// a test can assert that a human agent's turn never reached a model.
type countingProvider struct {
	mu    sync.Mutex
	calls []string
}

func (p *countingProvider) Chat(_ context.Context, messages []providers.Message, _ []providers.ToolDefinition, _ string, _ map[string]any) (*providers.LLMResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	last := ""
	if len(messages) > 0 {
		last = messages[len(messages)-1].Content
	}
	p.calls = append(p.calls, last)
	return &providers.LLMResponse{Content: "Alice here"}, nil
}

func (p *countingProvider) GetDefaultModel() string { return "test-model" }

func (p *countingProvider) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

const bobChat = "bob-chat"

// humanLoopConfig has an ordinary agent alice (the default, on the counting
// model) and a human agent bob reached in chat bob-chat on channel test. Bob
// allows every tool, so only his being a person keeps tools off him.
func humanLoopConfig(t *testing.T, timeoutSec int) *config.Config {
	t.Helper()
	cfg := newTestConfig(t)
	cfg.Providers = []config.Provider{{Name: "People", Protocol: config.HumanProtocol}}
	cfg.Models = []config.ModelConfig{{ModelName: "Bob (human)", Model: "bob", Provider: "People", Enabled: true, RequestTimeout: timeoutSec}}
	cfg.Agents.List = []config.AgentConfig{
		{ID: "alice", Name: "Alice", Default: true},
		{ID: "bob", Name: "Bob", Models: []string{"Bob (human)"}, Tools: []string{"*"}, MCPTools: []string{"*"}},
	}
	cfg.Bindings = []config.AgentBinding{{
		AgentID: "bob", Default: true,
		Match: config.BindingMatch{Channel: "test", Peer: &config.PeerMatch{Kind: "direct", ID: bobChat}},
	}}
	return cfg
}

func newHumanLoopWith(t *testing.T, cfg *config.Config) (*AgentLoop, *bus.MessageBus, *countingProvider) {
	t.Helper()
	msgBus := bus.NewMessageBus()
	model := &countingProvider{}
	return mustNewAgentLoop(t, cfg, msgBus, model, providers.NewProviderDispatcher(cfg)), msgBus, model
}

func newHumanLoop(t *testing.T, timeoutSec int) (*AgentLoop, *bus.MessageBus, *countingProvider) {
	t.Helper()
	return newHumanLoopWith(t, humanLoopConfig(t, timeoutSec))
}

// deliver passes msg on the way Run does (dispatchInbound), but waits for it.
func deliver(al *AgentLoop, msg bus.InboundMessage) {
	if al.takeHumanAnswer(context.Background(), msg) {
		return
	}
	dispatch(al, msg)
}

// fromBob is a message Bob writes in his own chat.
func fromBob(id, content string) bus.InboundMessage {
	return bus.InboundMessage{
		Channel: "test", ChatID: bobChat, SenderID: "bob-user", MessageID: id, Content: content,
		Peer: bus.Peer{Kind: "direct", ID: bobChat},
	}
}

// askBob is a question to bob on the ask channel, as core Ask sends it.
func askBob(id, content string) bus.InboundMessage {
	return bus.InboundMessage{
		Channel: constants.AgentMessageChannel, ChatID: "ask-" + id, SenderID: "Alice", MessageID: id,
		Content: askHeader(sender{name: "Alice"}) + "\n" + content,
		Metadata: map[string]string{
			metadataKeyPreresolvedAgentID: "bob",
			bus.MetaReplyRequired:         "1",
			metadataKeyAskFrom:            "Alice",
		},
	}
}

// sendAsk registers ask id as core Ask does and dispatches askBob(id,
// content) in the background; the returned channel gets the ask's reply.
func sendAsk(al *AgentLoop, id, content string) <-chan tools.AgentReply {
	replies := al.asks.open("ask-"+id, "Alice", time.Now().Add(time.Minute))
	go dispatch(al, askBob(id, content))
	return replies
}

// expectAskReply waits for the reply an ask's turn hands back to its asker.
func expectAskReply(t *testing.T, replies <-chan tools.AgentReply) tools.AgentReply {
	t.Helper()
	select {
	case r := <-replies:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("no reply to the ask")
		return tools.AgentReply{}
	}
}

// expectPosted reads the next outbound and checks it is a request in Bob's
// chat.
func expectPosted(t *testing.T, msgBus *bus.MessageBus) bus.OutboundMessage {
	t.Helper()
	posted := nextOutbound(t, msgBus)
	if posted.Channel != "test" || posted.ChatID != bobChat {
		t.Fatalf("expected a request in Bob's chat, got %+v", posted)
	}
	return posted
}

// expectInBobChat reads the next outbound and checks it is text in Bob's chat.
func expectInBobChat(t *testing.T, msgBus *bus.MessageBus, want string) {
	t.Helper()
	got := nextOutbound(t, msgBus)
	if got.ChatID != bobChat || got.Content != want {
		t.Fatalf("got %+v, want %q in Bob's chat", got, want)
	}
}

// An ask posts the request (with its sender header, nothing else) to the
// person's chat and returns their next message from it. A command sent while
// it waits is handled as a command and leaves the request waiting.
func TestHumanAgent_AskIsAnsweredByThePerson(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()
	al, msgBus, model := newHumanLoop(t, 60)

	replies := sendAsk(al, "r1", "Please review the draft.")
	if posted := expectPosted(t, msgBus); posted.Content != askHeader(sender{name: "Alice"})+"\nPlease review the draft." {
		t.Fatalf("request = %q, want the request with its sender header only", posted.Content)
	}

	deliver(al, fromBob("b0", "/help"))
	if got := nextOutbound(t, msgBus); got.ChatID != bobChat || !strings.Contains(got.Content, "/show") {
		t.Fatalf("/help while a request waits got %+v, want the help text", got)
	}

	deliver(al, fromBob("b1", "Looks good to me."))
	reply := expectAskReply(t, replies)
	if reply.Text != "Looks good to me." || reply.Outcome != bus.OutcomeOK {
		t.Fatalf("reply = %+v, want Bob's answer to r1", reply)
	}
	al.activeRequests.Wait()
	if n := model.count(); n != 0 {
		t.Fatalf("a model was called %d times for a human agent", n)
	}
}

// Only asks reach the person. Anything else routed to a human agent (an
// ordinary message, a scheduled job, a system notice) is dropped; a sender
// that requires a reply is told why.
func TestHumanAgent_OnlyAsksReachThePerson(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()
	al, msgBus, model := newHumanLoop(t, 60)

	internal := toAgent("bob", "m1", "hello") // claw's own, not an ask, no reply required
	internal.SenderID = "system"
	dispatch(al, internal)
	noOutbound(t, msgBus)

	plain := askBob("m2", "hello")
	plain.Channel = "test"
	dispatch(al, plain) // reply required, but not on the ask channel
	got := nextOutbound(t, msgBus)
	if got.ChatID == bobChat || got.Content != "Bob only answers questions from agents." || got.Outcome != bus.OutcomeError {
		t.Fatalf("non-ask got %+v", got)
	}

	cron := fromBob("j1", "Time to submit the report.")
	cron.SenderID = "cron"
	dispatch(al, cron) // routed to bob by his binding
	noOutbound(t, msgBus)
	if n := model.count(); n != 0 {
		t.Fatalf("a model was called %d times", n)
	}
}

// No answer before the model's request timeout: the reply is empty. A late
// answer is told the request timed out; once the window has passed it is
// told nothing is waiting.
func TestHumanAgent_TimeoutIsEmpty(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()
	al, msgBus, model := newHumanLoop(t, 1)

	replies := sendAsk(al, "r1", "Are you there?")
	expectPosted(t, msgBus)
	reply := expectAskReply(t, replies)
	if reply.Text != "" || reply.Outcome != bus.OutcomeEmpty {
		t.Fatalf("reply = %+v, want an empty reply to r1", reply)
	}
	al.activeRequests.Wait()

	deliver(al, fromBob("b1", "Sorry, I was away."))
	expectInBobChat(t, msgBus, timedOutReply)
	time.Sleep(1100 * time.Millisecond)
	deliver(al, fromBob("b2", "Hello?"))
	expectInBobChat(t, msgBus, nothingWaitingReply)
	if n := model.count(); n != 0 {
		t.Fatalf("a model was called %d times", n)
	}
}

// A message from the person that answers nothing starts no turn; a command
// still reaches the command path, and only "/" starts one in their chat.
func TestHumanAgent_UnsolicitedMessages(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()
	al, msgBus, model := newHumanLoop(t, 60)

	deliver(al, fromBob("b1", "Hello?"))
	expectInBobChat(t, msgBus, nothingWaitingReply)

	deliver(al, fromBob("b2", "!help"))
	expectInBobChat(t, msgBus, nothingWaitingReply)

	deliver(al, fromBob("b3", "/help"))
	if got := nextOutbound(t, msgBus); got.ChatID != bobChat || !strings.Contains(got.Content, "/show") {
		t.Fatalf("/help got %+v, want the help text", got)
	}

	// /clear runs, but no notice is posted to the person.
	deliver(al, fromBob("b4", "/clear"))
	nextOutbound(t, msgBus)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if msg, ok := msgBus.ConsumeInbound(ctx); ok {
		t.Fatalf("/clear published %+v for a human agent", msg)
	}
	al.activeRequests.Wait()
	if n := model.count(); n != 0 {
		t.Fatalf("a model was called %d times", n)
	}
}

// An attachment alone does not answer; an answer starting with "!" does.
func TestHumanAgent_AnswerMustBeText(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()
	al, msgBus, _ := newHumanLoop(t, 60)

	replies := sendAsk(al, "r1", "Which option?")
	expectPosted(t, msgBus)
	photo := fromBob("b1", "")
	photo.Media = []string{"media://photo"}
	deliver(al, photo)
	expectInBobChat(t, msgBus, textOnlyReply)

	deliver(al, fromBob("b2", "!B, clearly"))
	if reply := expectAskReply(t, replies); reply.Text != "!B, clearly" {
		t.Fatalf("reply = %+v, want the text answer", reply)
	}
	al.activeRequests.Wait()
}

// /cancel from the person ends the waiting request as cancelled.
func TestHumanAgent_PersonCancels(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()
	al, msgBus, _ := newHumanLoop(t, 60)

	replies := sendAsk(al, "r1", "Can you take this?")
	expectPosted(t, msgBus)
	deliver(al, fromBob("b1", "/cancel"))
	expectInBobChat(t, msgBus, cancelledReply)
	reply := expectAskReply(t, replies)
	if reply.Outcome != tools.OutcomePersonCancelled || reply.Text != "Bob cancelled the request." {
		t.Fatalf("reply = %+v, want a cancelled reply to r1", reply)
	}
	al.activeRequests.Wait()
}

// Shutdown during a request ends it as cancelled: the sender gets its final
// reply and nothing is left to replay.
func TestHumanAgent_ShutdownCancels(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()
	al, msgBus, _ := newHumanLoop(t, 60)

	ctx, stop := context.WithCancelCause(context.Background())
	al.activeRequests.Add(1)
	replies := al.asks.open("ask-r1", "Alice", time.Now().Add(time.Minute))
	go al.processSessionMessage(ctx, askBob("r1", "Still there?"))
	expectPosted(t, msgBus)
	stop(errShuttingDown)
	reply := expectAskReply(t, replies)
	if reply.Outcome != bus.OutcomeCancelled {
		t.Fatalf("reply = %+v, want a cancelled reply to r1", reply)
	}
	expectInBobChat(t, msgBus, "Alice no longer needs an answer to that request.")
	al.activeRequests.Wait()
	bob, _ := al.GetRegistry().Get("bob")
	keys, err := bob.Sessions.ListPendingSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 0 {
		t.Fatalf("the request was left to replay: %v", keys)
	}
}

// Other agents and chats are unchanged: a message in another chat is an
// ordinary model turn, and a message bound for the person's chat by claw
// itself is not taken for the person's.
func TestHumanAgent_OtherTrafficUnchanged(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()
	al, msgBus, model := newHumanLoop(t, 60)

	dispatch(al, inbound("c2", "a1", "Hi Alice"))
	if got := nextOutbound(t, msgBus); got.ChatID != "c2" || got.Content != "Alice here" {
		t.Fatalf("ordinary turn got %+v", got)
	}
	if n := model.count(); n != 1 {
		t.Fatalf("model calls = %d, want 1", n)
	}
	cron := fromBob("j1", "x")
	cron.SenderID = "cron"
	if _, ok := al.humanChatOwner(cron); ok {
		t.Fatal("a scheduled job's message was taken for the person's own")
	}
	if _, ok := al.humanChatOwner(fromBob("b1", "x")); !ok {
		t.Fatal("the person's message was not recognised")
	}
}

// The chat of a human agent that is not running never reaches another agent.
func TestHumanAgent_NotRunningChatIsKept(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()
	cfg := humanLoopConfig(t, 60)
	off := false
	cfg.Agents.List[1].Enabled = &off
	al, msgBus, model := newHumanLoopWith(t, cfg)

	deliver(al, fromBob("b1", "Hello?"))
	expectInBobChat(t, msgBus, "Bob is not running.")
	deliver(al, fromBob("b2", "/help"))
	expectInBobChat(t, msgBus, "Bob is not running.")
	if n := model.count(); n != 0 {
		t.Fatalf("the person's message reached a model %d times", n)
	}
}

// Requests to one human agent are answered one at a time.
func TestHumanAgent_OneRequestAtATime(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()
	al, msgBus, _ := newHumanLoop(t, 60)
	bob, _ := al.GetRegistry().Get("bob")
	key := routing.BuildAgentMainSessionKey("bob")

	results := make(chan string, 2)
	for _, q := range []string{"first", "second"} {
		al.asks.open("ask-"+q, "Alice", time.Now().Add(time.Minute))
		go func() {
			out, err := al.runAgentLoop(context.Background(), bob, processOptions{
				SessionKey: key, Channel: constants.AgentMessageChannel, ChatID: "ask-" + q, UserMessage: q, ReplyRequired: true,
			})
			if err != nil {
				t.Errorf("runAgentLoop: %v", err)
			}
			results <- out
		}()
	}
	first := expectPosted(t, msgBus)
	noOutbound(t, msgBus) // the second waits
	deliver(al, fromBob("b1", "answer to "+first.Content))
	second := expectPosted(t, msgBus)
	if second.Content == first.Content {
		t.Fatalf("the same request was posted twice: %q", first.Content)
	}
	deliver(al, fromBob("b2", "answer to "+second.Content))
	got := map[string]bool{}
	for range 2 {
		select {
		case r := <-results:
			got[r] = true
		case <-time.After(5 * time.Second):
			t.Fatal("a request was never answered")
		}
	}
	if !got["answer to first"] || !got["answer to second"] {
		t.Fatalf("answers = %v, want each request its own answer", got)
	}
}

// A request's timeout runs from when it is posted, not from when it queued
// behind another: queued for longer than its timeout, it still waits once
// posted.
func TestHumanAgent_TimeoutRunsFromPosting(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()
	al, msgBus, _ := newHumanLoop(t, 60)
	desk := al.humans.slot("bob")
	desk <- struct{}{} // another request holds the person

	done := make(chan error, 1)
	go func() {
		_, err := al.askHuman(context.Background(), "bob", "test", bobChat, "question", 300*time.Millisecond, nil)
		done <- err
	}()
	time.Sleep(600 * time.Millisecond) // queued twice as long as its timeout
	<-desk
	expectPosted(t, msgBus)
	if !al.humans.waiting("bob") {
		t.Fatal("the request expired before it was posted")
	}
	if err := <-done; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want its own timeout after posting", err)
	}
}

// A waiting request does not hold a concurrent-turn slot.
func TestHumanAgent_WaitDoesNotHoldTurnSlot(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()
	cfg := humanLoopConfig(t, 60)
	cfg.Agents.Defaults.MaxConcurrentTurns = 1
	al, msgBus, _ := newHumanLoopWith(t, cfg)

	replies := sendAsk(al, "r1", "Take your time.")
	expectPosted(t, msgBus)
	go dispatch(al, inbound("c2", "a1", "Hi Alice"))
	if got := nextOutbound(t, msgBus); got.ChatID != "c2" || got.Content != "Alice here" {
		t.Fatalf("Alice's turn got %+v while Bob's request waited", got)
	}
	deliver(al, fromBob("b1", "Done."))
	if got := expectAskReply(t, replies); got.Text != "Done." {
		t.Fatalf("reply = %+v", got)
	}
	al.activeRequests.Wait()
	select {
	case al.turnSem <- struct{}{}: // the slot is free again
		<-al.turnSem
	default:
		t.Fatal("the turn slot was not returned")
	}
}

// An answer that arrived while the wait was ending is delivered, not lost.
func TestHumanAgent_BufferedAnswerSurvivesCancel(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()
	al, msgBus, _ := newHumanLoop(t, 60)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		answer string
		err    error
	}
	done := make(chan result, 1)
	go func() {
		a, err := al.askHuman(ctx, "bob", "test", bobChat, "question", time.Minute, nil)
		done <- result{a, err}
	}()
	expectPosted(t, msgBus)
	if !al.takeHumanAnswer(ctx, fromBob("b1", "yes")) {
		t.Fatal("answer not taken")
	}
	cancel()
	if r := <-done; r.err != nil || r.answer != "yes" {
		t.Fatalf("askHuman = %q, %v; want the buffered answer", r.answer, r.err)
	}
}

// The dispatched model must be a person's model.
func TestHumanAgent_RefusesNonHumanProvider(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()
	cfg := humanLoopConfig(t, 60)
	other := humanLoopConfig(t, 60)
	other.Providers = []config.Provider{{Name: "People", Protocol: "openai-chat", BaseURL: "http://127.0.0.1:1", APIKey: "k"}}
	al := mustNewAgentLoop(t, cfg, bus.NewMessageBus(), &countingProvider{}, providers.NewProviderDispatcher(other))
	bob, _ := al.GetRegistry().Get("bob")
	al.asks.open("ask-1", "Alice", time.Now().Add(time.Minute))
	_, err := al.runAgentLoop(context.Background(), bob, processOptions{
		SessionKey: "agent:bob:main", Channel: constants.AgentMessageChannel, ChatID: "ask-1", UserMessage: "q", ReplyRequired: true,
	})
	if err == nil || !strings.Contains(err.Error(), "is not a person's model") {
		t.Fatalf("err = %v", err)
	}
}

// A human agent has no tools (not even ones registered later, nor MCP tools)
// and no cognitive memory, and none of the host's model paths
// (summarization, consolidation, vision) has a model for it.
func TestHumanAgent_NoModelPathsNoTools(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()
	al, _, model := newHumanLoop(t, 60)
	bob, _ := al.GetRegistry().Get("bob")
	alice, _ := al.GetRegistry().Get("alice")

	if bob.HumanModel != "Bob (human)" || alice.HumanModel != "" {
		t.Fatalf("HumanModel: bob %q, alice %q", bob.HumanModel, alice.HumanModel)
	}
	al.RegisterTool(&noopWriteFile{})
	conn := &climcp.ServerConnection{Name: "srv", Tools: []mcpgo.Tool{mcpgo.NewTool("lookup")}}
	if n := registerMCPServerToolsOn(bob, nil, "srv", conn); n != 0 {
		t.Errorf("MCP refresh registered %d tools on a human agent", n)
	}
	if n := bob.Tools.Count(); n != 0 {
		t.Errorf("a human agent has %d tools: %v", n, bob.Tools.List())
	}
	if _, ok := alice.Tools.Get("file_write"); !ok {
		t.Error("an ordinary agent did not get a registered tool")
	}
	if bob.Config.CognitiveMemoryEnabled() {
		t.Error("a human agent has cognitive memory")
	}
	if !alice.Config.CognitiveMemoryEnabled() {
		t.Error("an ordinary agent lost its cognitive memory")
	}
	caller, _ := al.newCompressModelCaller(bob, "s")
	if len(caller.clients) != 0 {
		t.Errorf("a human agent has %d summarization clients", len(caller.clients))
	}
	if _, _, _, err := caller.complete(context.Background(), "sys", "user", false, nil); err == nil {
		t.Error("summarizing a human agent's conversation found a model")
	}
	if c, _ := al.newCompressModelCaller(alice, "s"); len(c.clients) == 0 {
		t.Error("an ordinary agent lost its summarization chain")
	}
	bob.VisionClients = []visionClient{panicVision{}}
	if _, ok := al.describeImages(context.Background(), bob, []string{"data:image/png;base64,AA=="}, ""); ok {
		t.Error("a human agent's images were described by a model")
	}
	if n := model.count(); n != 0 {
		t.Fatalf("a model was called %d times", n)
	}
}

// panicVision is a vision client that must never be called.
type panicVision struct{}

func (panicVision) Complete(context.Context, []providers.Message) (visionReply, error) {
	panic("a vision model was called for a human agent")
}

// A human agent is never the default agent, even first in the list.
func TestHumanAgent_NeverDefault(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()
	cfg := humanLoopConfig(t, 60)
	cfg.Agents.List = []config.AgentConfig{cfg.Agents.List[1], cfg.Agents.List[0]}
	cfg.Agents.List[1].Default = false
	al, _, _ := newHumanLoopWith(t, cfg)
	if got := al.GetRegistry().DefaultID(); got != "alice" {
		t.Errorf("default agent = %q, want alice", got)
	}
	if got := routing.NewRouteResolver(cfg).ResolveRoute(routing.RouteInput{Channel: "other"}).AgentID; got != "alice" {
		t.Errorf("unbound message routed to %q, want alice", got)
	}
}

// A person is never cloned or spawned, and no temporary agent runs on a
// human model.
func TestHumanAgent_NeverClonedOrSpawned(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()
	al, _, _ := newHumanLoop(t, 60)
	registry := al.GetRegistry()

	if _, err := registry.Create(config.AgentConfig{}, agentreg.CloneOf("bob")); !errors.Is(err, agentreg.ErrHuman) {
		t.Errorf("clone of bob: err = %v, want ErrHuman", err)
	}
	if _, err := registry.Create(config.AgentConfig{Models: []string{"Bob (human)"}}); !errors.Is(err, agentreg.ErrHuman) {
		t.Errorf("fresh agent on Bob's model: err = %v, want ErrHuman", err)
	}
	if _, err := newAgentServices(al, "bob").CreateFresh("Bob (human)"); !errors.Is(err, agentreg.ErrHuman) {
		t.Errorf("CreateFresh on Bob's model: err = %v, want ErrHuman", err)
	}
	_, release, err := al.runSubagentTask(context.Background(), "bob", "do it", "", nil)
	release()
	if err == nil || !strings.Contains(err.Error(), "represents a person and cannot be spawned") {
		t.Errorf("spawn of bob: err = %v", err)
	}
	if n := len(registry.ListTemp()); n != 0 {
		t.Fatalf("%d temporary agents exist", n)
	}
	id, err := registry.Create(config.AgentConfig{}, agentreg.CloneOf("alice"))
	if err != nil {
		t.Fatalf("clone of alice: %v", err)
	}
	if err := registry.Delete(id); err != nil {
		t.Errorf("Delete clone of alice: %v", err)
	}
}

// External messages are refused for a human agent.
func TestHumanAgent_ExternalMessagesRefused(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()
	al, msgBus, _ := newHumanLoop(t, 60)
	if err := al.HandleExternalMessage(context.Background(), "bob", "alarm"); !errors.Is(err, ErrHumanAgent) {
		t.Fatalf("err = %v, want ErrHumanAgent", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if msg, ok := msgBus.ConsumeInbound(ctx); ok {
		t.Fatalf("published %+v", msg)
	}
}

// fakeDismisser records the chats whose indicators were cleared.
type fakeDismisser struct {
	mu   sync.Mutex
	seen []string
	done chan struct{}
}

func (f *fakeDismisser) DismissInbound(_ context.Context, channel, chatID, messageID string) {
	f.mu.Lock()
	f.seen = append(f.seen, channel+":"+chatID+":"+messageID)
	f.mu.Unlock()
	f.done <- struct{}{}
}

// An accepted answer clears the chat's typing, reaction and placeholder, and
// nothing is sent for it.
func TestHumanAgent_AnswerDismissesIndicators(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()
	al, msgBus, _ := newHumanLoop(t, 60)
	fake := &fakeDismisser{done: make(chan struct{}, 1)}
	al.dismisser = fake

	replies := sendAsk(al, "r1", "Ready?")
	expectPosted(t, msgBus)
	deliver(al, fromBob("b1", "Yes."))
	select {
	case <-fake.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the answer's indicators were not cleared")
	}
	if fake.seen[0] != "test:"+bobChat+":b1" {
		t.Fatalf("dismissed %v", fake.seen)
	}
	if reply := expectAskReply(t, replies); reply.Text != "Yes." {
		t.Fatalf("got %+v, want the answer as the ask's reply", reply)
	}
	noOutbound(t, msgBus)
	al.activeRequests.Wait()
}

// Someone who writes to a human agent directly (a mention, a chat, a device)
// is told it only answers agents' questions, never left without a reply.
func TestHumanAgent_PersonWritingToItIsTold(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()
	cfg := humanLoopConfig(t, 60)
	cfg.Bindings = append(cfg.Bindings, config.AgentBinding{
		AgentID: "alice", AgentMentions: []string{"*"}, Match: config.BindingMatch{Channel: "test"},
	})
	al, msgBus, model := newHumanLoopWith(t, cfg)

	dispatch(al, inbound("c2", "a1", "@bob are you free?"))
	got := nextOutbound(t, msgBus)
	if got.ChatID != "c2" || got.OriginalMessageID != "a1" || got.Content != "Bob only answers questions from agents." {
		t.Fatalf("mention of bob got %+v", got)
	}
	dispatch(al, toAgent("bob", "a2", "hello")) // addressed to bob by a person's client
	if got := nextOutbound(t, msgBus); got.Content != "Bob only answers questions from agents." {
		t.Fatalf("message to bob got %+v", got)
	}
	if n := model.count(); n != 0 {
		t.Fatalf("a model was called %d times", n)
	}
}

// An unknown command in the person's chat gets the usual reply, whether or
// not a request is waiting.
func TestHumanAgent_UnknownCommand(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()
	al, msgBus, _ := newHumanLoop(t, 60)

	deliver(al, fromBob("b1", "/nosuch"))
	if got := nextOutbound(t, msgBus); got.ChatID != bobChat || !strings.Contains(got.Content, "Unknown command") {
		t.Fatalf("idle: got %+v", got)
	}
	replies := sendAsk(al, "r1", "Ready?")
	expectPosted(t, msgBus)
	deliver(al, fromBob("b2", "/nosuch"))
	if got := nextOutbound(t, msgBus); got.ChatID != bobChat || !strings.Contains(got.Content, "Unknown command") {
		t.Fatalf("waiting: got %+v", got)
	}
	deliver(al, fromBob("b3", "Ready."))
	if got := expectAskReply(t, replies); got.Text != "Ready." {
		t.Fatalf("reply = %+v", got)
	}
	al.activeRequests.Wait()
}

// A request the channel could not post to the person's chat ends the ask at
// once with an error naming the agent and saying why, from the channel's
// reason.
func TestHumanAgent_UnreachableChatSaysWhy(t *testing.T) {
	tests := []struct {
		name  string
		cause error
		want  string
	}{
		{"unknown channel", fmt.Errorf("%w: telegram-x", channels.ErrUnknownChannel), "Bob's chat is not set up."},
		{"channel not running", channels.ErrNotRunning, "Bob's chat is unavailable."},
		{"recipient offline", fmt.Errorf("device:1: %w", channels.ErrRecipientOffline), "Bob's device is offline."},
		{"recipient not found", fmt.Errorf("chat not found: %w", channels.ErrRecipientNotFound), "Bob's chat can't be reached."},
		{"failed after retries", fmt.Errorf("timeout: %w", channels.ErrTemporary), "Couldn't reach Bob's chat."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			restore := logger.RedirectForTest(&safeBufLoop{})
			defer restore()
			al, msgBus, _ := newHumanLoop(t, 60)

			replies := sendAsk(al, "r1", "Are you there?")
			posted := expectPosted(t, msgBus)
			posted.OnDelivery(tt.cause)

			reply := expectAskReply(t, replies)
			if reply.Text != tt.want || reply.Outcome != tools.OutcomePersonUnreachable {
				t.Fatalf("reply = %+v, want %q", reply, tt.want)
			}
			al.activeRequests.Wait()
		})
	}
}

// A request the channel could not post to the person's chat ends the ask at
// once with an error naming the agent, not after the timeout.
func TestHumanAgent_UnreachableChatEndsAtOnce(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()
	al, msgBus, _ := newHumanLoop(t, 60)

	start := time.Now()
	replies := sendAsk(al, "r1", "Are you there?")
	posted := expectPosted(t, msgBus)
	if posted.OnDelivery == nil {
		t.Fatal("the request must ask for its delivery to be reported")
	}
	posted.OnDelivery(errors.New("send failed"))

	reply := expectAskReply(t, replies)
	if reply.Text != "Couldn't reach Bob's chat." || reply.Outcome != tools.OutcomePersonUnreachable {
		t.Fatalf("reply = %+v, want the unreachable error naming Bob", reply)
	}
	if waited := time.Since(start); waited > 5*time.Second {
		t.Fatalf("the ask waited %s, want it to end at once", waited)
	}
	al.activeRequests.Wait()

	// The person never saw the request, so a later message answers nothing.
	deliver(al, fromBob("b1", "Hello?"))
	expectInBobChat(t, msgBus, nothingWaitingReply)
}

// A successful delivery report changes nothing: the request keeps waiting
// for the person's answer.
func TestHumanAgent_DeliveredRequestKeepsWaiting(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()
	al, msgBus, _ := newHumanLoop(t, 60)

	replies := sendAsk(al, "r1", "Are you there?")
	posted := expectPosted(t, msgBus)
	posted.OnDelivery(nil)
	deliver(al, fromBob("b1", "Yes."))
	if reply := expectAskReply(t, replies); reply.Text != "Yes." || reply.Outcome != bus.OutcomeOK {
		t.Fatalf("reply = %+v, want Bob's answer", reply)
	}
	al.activeRequests.Wait()
}
