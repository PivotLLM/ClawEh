// ClawEh
// License: MIT

package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/providers"
	"github.com/PivotLLM/ClawEh/tools"
	toolsagents "github.com/PivotLLM/ClawEh/tools/agents"
)

// Agent messaging (Ask, Whisper, agent_message, /ask) with a human agent at
// the other end: the person is asked through their chat and their answer is
// the ask's reply.

// humanMessagingConfig is humanLoopConfig with agent_message on, Alice
// allowed to message Bob, and an ordinary agent helper. Bob is allowed to
// message everyone, so only his being a person keeps the tool off him.
func humanMessagingConfig(t *testing.T, timeoutSec int) *config.Config {
	t.Helper()
	cfg := humanLoopConfig(t, timeoutSec)
	cfg.Tools.Subagent.Enabled = true
	cfg.Tools.Overrides = map[string]bool{"agent_message": true}
	cfg.Agents.List[0].Subagents = &config.SubagentsConfig{AllowAgents: []string{"bob"}}
	cfg.Agents.List[1].Subagents = &config.SubagentsConfig{AllowAgents: []string{"*"}}
	cfg.Agents.List = append(cfg.Agents.List, config.AgentConfig{ID: "helper", Name: "Helper"})
	return cfg
}

// humanMessagingLoop runs a loop over cfg whose model agents answer with
// model; Bob's turns go to the person through the provider dispatcher.
func humanMessagingLoop(t *testing.T, cfg *config.Config, model providers.LLMProvider) (*AgentLoop, *bus.MessageBus) {
	t.Helper()
	return messagingLoopWith(t, cfg, model, providers.NewProviderDispatcher(cfg))
}

func publishIn(t *testing.T, msgBus *bus.MessageBus, msg bus.InboundMessage) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := msgBus.PublishInbound(ctx, msg); err != nil {
		t.Fatalf("publish inbound: %v", err)
	}
}

// askInBackground runs al.Ask from Alice and returns where its result lands.
func askInBackground(ctx context.Context, al *AgentLoop, message string, wait time.Duration) <-chan tools.AgentReply {
	out := make(chan tools.AgentReply, 1)
	go func() {
		reply, err := al.Ask(ctx, "Alice", "bob", message, wait)
		if err != nil {
			reply = tools.AgentReply{Outcome: "go-error", Text: err.Error()}
		}
		out <- reply
	}()
	return out
}

// Alice's agent_message ask reaches Bob's chat as the ask's request; Bob's
// answer comes back as the tool's result and Alice's turn goes on with it.
// With one concurrent-turn slot, Alice's turn lends it while the person
// types, so another agent's turn runs meanwhile; Bob's asked turn takes none,
// and the slot is free once everything is over.
func TestHumanAgent_AgentMessageAskIsAnswered(t *testing.T) {
	t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))
	cfg := humanMessagingConfig(t, 60)
	cfg.Agents.Defaults.MaxConcurrentTurns = 1
	al, msgBus := humanMessagingLoop(t, cfg,
		callThenText("agent_message", `{"agent":"bob","message":"Please review the draft.","wait_seconds":30}`))
	setModel(t, al, "helper", chatFunc(func(context.Context, []providers.Message) (*providers.LLMResponse, error) {
		return &providers.LLMResponse{Content: "Helper here"}, nil
	}))

	publishIn(t, msgBus, inbound("c2", "a1", "Get Bob's review"))
	if posted := expectPosted(t, msgBus); posted.Content != askHeader(sender{name: "Alice"})+"\nPlease review the draft." {
		t.Fatalf("Bob was shown %q", posted.Content)
	}

	publishIn(t, msgBus, toAgent("helper", "h1", "hi"))
	if got := nextOutbound(t, msgBus); got.Content != "Helper here" {
		t.Fatalf("Helper's turn got %+v while Alice waited on Bob", got)
	}

	publishIn(t, msgBus, fromBob("b1", "Looks good."))
	if got := nextOutbound(t, msgBus); got.ChatID != "c2" || got.Content != "done: Looks good." || got.Outcome != bus.OutcomeOK {
		t.Fatalf("Alice's reply = %+v, want Bob's answer carried into it", got)
	}
	noOutbound(t, msgBus)
	al.activeRequests.Wait()
	select {
	case al.turnSem <- struct{}{}:
		<-al.turnSem
	default:
		t.Fatal("the turn slot was not returned")
	}
}

// An ask the person leaves unanswered times out for the asker; the person's
// late answer is told the request timed out.
func TestHumanAgent_AskTimesOut(t *testing.T) {
	t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))
	al, msgBus := humanMessagingLoop(t, humanMessagingConfig(t, 1), &countingProvider{})

	result := askInBackground(context.Background(), al, "Are you there?", 30*time.Second)
	expectPosted(t, msgBus)
	select {
	case reply := <-result:
		if reply.Outcome != tools.OutcomeTimeout || reply.Text != "Bob did not reply within 1 second." {
			t.Fatalf("reply = %+v, want a timeout", reply)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the ask never returned")
	}
	deadline := time.Now().Add(5 * time.Second)
	for al.humans.waiting("bob") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	publishIn(t, msgBus, fromBob("b1", "Sorry, I was away."))
	expectInBobChat(t, msgBus, timedOutReply)
}

// /ask from a chat allowed to reach Bob (a mention binding) posts the
// question to Bob's chat, marked as from a person, and Bob's answer to the
// asking chat, attributed to Bob.
func TestHumanAgent_SlashAskFromAllowedChat(t *testing.T) {
	t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))
	cfg := humanMessagingConfig(t, 60)
	cfg.Bindings = append(cfg.Bindings, config.AgentBinding{
		AgentID: "alice", AgentMentions: []string{"bob"},
		Match: config.BindingMatch{Channel: "test", Peer: &config.PeerMatch{Kind: "direct", ID: "c2"}},
	})
	_, msgBus := humanMessagingLoop(t, cfg, &countingProvider{})

	publishIn(t, msgBus, bus.InboundMessage{
		Channel: "test", ChatID: "c2", SenderID: "u2", MessageID: "a1", Content: "/ask bob Can you check the figures?",
		Peer: bus.Peer{Kind: "direct", ID: "c2"}, Sender: bus.SenderInfo{DisplayName: "Alice"},
	})
	want := askHeader(sender{name: "Alice", note: personNote("ask", "test")}) + "\nCan you check the figures?"
	if posted := expectPosted(t, msgBus); posted.Content != want {
		t.Fatalf("Bob was shown %q, want %q", posted.Content, want)
	}
	publishIn(t, msgBus, fromBob("b1", "They add up."))
	if got := nextOutbound(t, msgBus); got.ChatID != "c2" || got.Content != "Bob: They add up." {
		t.Fatalf("/ask reply = %+v", got)
	}

	publishIn(t, msgBus, bus.InboundMessage{
		Channel: "test", ChatID: "c3", SenderID: "u3", MessageID: "a2", Content: "/ask bob hello",
		Peer: bus.Peer{Kind: "direct", ID: "c3"},
	})
	if got := nextOutbound(t, msgBus); got.ChatID != "c3" || got.Content != "You don't have permission to /ask Bob" {
		t.Fatalf("/ask from a chat not allowed to reach Bob got %+v", got)
	}
}

// A whisper to Bob waits for the next ask that reaches the person and opens
// the request they see, once, without the hint on answering (Bob has no
// tools). A message dropped because it is not an ask does not take it.
func TestHumanAgent_WhisperDeliveredWithNextAsk(t *testing.T) {
	t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))
	al, msgBus := humanMessagingLoop(t, humanMessagingConfig(t, 60), &countingProvider{})

	if err := al.Whisper(context.Background(), "Alice", "bob", "The deadline moved to Friday."); err != nil {
		t.Fatalf("Whisper: %v", err)
	}
	noOutbound(t, msgBus)
	internal := toAgent("bob", "m1", "hello")
	internal.SenderID = "system"
	dispatch(al, internal) // dropped: not an ask
	noOutbound(t, msgBus)

	head := askHeader(sender{name: "Alice"})
	first := askInBackground(context.Background(), al, "Can you review it?", 30*time.Second)
	want := "[Private whisper from Alice — no reply expected.] The deadline moved to Friday.\n\n" + head + "\nCan you review it?"
	if posted := expectPosted(t, msgBus); posted.Content != want {
		t.Fatalf("Bob was shown %q, want %q", posted.Content, want)
	}
	publishIn(t, msgBus, fromBob("b1", "Yes."))
	if r := <-first; r.Outcome != bus.OutcomeOK || r.Text != "Yes." {
		t.Fatalf("reply = %+v", r)
	}

	second := askInBackground(context.Background(), al, "Done?", 30*time.Second)
	if posted := expectPosted(t, msgBus); posted.Content != head+"\nDone?" {
		t.Fatalf("the whisper was delivered twice: %q", posted.Content)
	}
	publishIn(t, msgBus, fromBob("b2", "Done."))
	if r := <-second; r.Text != "Done." {
		t.Fatalf("reply = %+v", r)
	}
}

// A human agent never gets agent_message, whatever its allow list: it has no
// tools, so it can never ask or whisper back.
func TestHumanAgent_HasNoAgentMessage(t *testing.T) {
	t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))
	al, _ := humanMessagingLoop(t, humanMessagingConfig(t, 60), &countingProvider{})
	bob, _ := al.GetRegistry().Get("bob")
	if _, ok := bob.Tools.Get("agent_message"); ok {
		t.Fatal("a human agent has agent_message")
	}
	alice, _ := al.GetRegistry().Get("alice")
	if _, ok := alice.Tools.Get("agent_message"); !ok {
		t.Fatal("alice lacks agent_message: the check above proves nothing")
	}
}

// An ask from a remote, nested exchange reaches the person exactly as any
// other: the depth, chain and remote mark ride on the turn, not on what the
// person sees, and the answer comes back ok.
func TestHumanAgent_RemoteOriginAndDepthPassThrough(t *testing.T) {
	t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))
	al, msgBus := humanMessagingLoop(t, humanMessagingConfig(t, 60), &countingProvider{})

	ctx := tools.WithRemoteOrigin(tools.WithAskChain(toolsagents.WithSpawnDepth(context.Background(), 1), []string{"alice"}))
	result := askInBackground(ctx, al, "Approve?", 30*time.Second)
	posted := expectPosted(t, msgBus)
	if posted.Content != askHeader(sender{name: "Alice"})+"\nApprove?" {
		t.Fatalf("Bob was shown %q", posted.Content)
	}
	if strings.Contains(posted.Content, "remote") {
		t.Fatalf("the request carried the exchange's marks: %+v", posted)
	}
	publishIn(t, msgBus, fromBob("b1", "Approved."))
	if r := <-result; r.Outcome != bus.OutcomeOK || r.Text != "Approved." {
		t.Fatalf("reply = %+v", r)
	}
}
