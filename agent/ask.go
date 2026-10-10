// ClawEh
// License: MIT

package agent

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/constants"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/routing"
	"github.com/PivotLLM/ClawEh/tools"
	toolsagents "github.com/PivotLLM/ClawEh/tools/agents"
)

// metadataKeyAskChain carries, on an ask's inbound message, the agents
// already waiting in the exchange (comma-separated ids), so the asked agent
// cannot ask any of them back: each would wait on the other until the
// timeout.
const metadataKeyAskChain = "ask_chain"

// metadataKeyAskFrom carries, on an ask's inbound message, the asker's name,
// so a person whose answer arrives after the asker stopped waiting can be
// told who no longer needs it.
const metadataKeyAskFrom = "ask_from"

// withInboundAskChain puts the chain an ask's message carries on ctx.
func withInboundAskChain(ctx context.Context, msg bus.InboundMessage) context.Context {
	raw := inboundMetadata(msg, metadataKeyAskChain)
	if raw == "" {
		return ctx
	}
	chain := slices.Clone(tools.AskChain(ctx))
	for id := range strings.SplitSeq(raw, ",") {
		if id = strings.TrimSpace(id); id != "" && !slices.Contains(chain, id) {
			chain = append(chain, id)
		}
	}
	return tools.WithAskChain(ctx, chain)
}

// askRegistry holds the asks waiting for their reply, by ask id (the chat id
// of the ask's message). The zero value is ready to use.
type askRegistry struct {
	mu      sync.Mutex
	waiting map[string]*pendingAsk
}

// pendingAsk is one ask whose asker is waiting.
type pendingAsk struct {
	replies  chan tools.AgentReply // buffered: one reply
	from     string                // the asker's name
	deadline time.Time             // when the asker stops waiting
	gone     chan struct{}         // closed once the ask is answered or given up
	stopTurn bool                  // the asked turn is cancelled once the asker gives up
}

// askWait is what a turn answering an ask knows of its asker: who it is,
// when it stops waiting, and a channel closed once it has stopped.
type askWait struct {
	from     string
	deadline time.Time
	gone     <-chan struct{}
	stopTurn bool // the turn is to be cancelled once gone is closed
}

// open registers ask id from the asker from, who waits until deadline, and
// returns the channel its reply arrives on. With stopTurn, the asked turn is
// cancelled once the asker stops waiting.
func (r *askRegistry) open(id, from string, deadline time.Time, stopTurn bool) <-chan tools.AgentReply {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.waiting == nil {
		r.waiting = make(map[string]*pendingAsk)
	}
	p := &pendingAsk{replies: make(chan tools.AgentReply, 1), from: from, deadline: deadline, gone: make(chan struct{}), stopTurn: stopTurn}
	r.waiting[id] = p
	return p.replies
}

// takeLocked removes ask id and marks it gone.
func (r *askRegistry) takeLocked(id string) (*pendingAsk, bool) {
	p, ok := r.waiting[id]
	if ok {
		delete(r.waiting, id)
		close(p.gone)
	}
	return p, ok
}

// close forgets ask id; a reply arriving later is discarded.
func (r *askRegistry) close(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.takeLocked(id)
}

// deliver hands reply to ask id, once. It reports false when nobody is
// waiting (the asker gave up, or the process restarted).
func (r *askRegistry) deliver(id string, reply tools.AgentReply) bool {
	r.mu.Lock()
	p, ok := r.takeLocked(id)
	r.mu.Unlock()
	if !ok {
		return false
	}
	p.replies <- reply
	return true
}

// isWaiting reports whether ask id still has an asker waiting.
func (r *askRegistry) isWaiting(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.waiting[id]
	return ok
}

// wait returns what is known of ask id's asker, or false when nobody waits.
func (r *askRegistry) wait(id string) (askWait, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.waiting[id]
	if !ok {
		return askWait{}, false
	}
	return askWait{from: p.from, deadline: p.deadline, gone: p.gone, stopTurn: p.stopTurn}, true
}

// waitGraph records which agent's turn is waiting on an ask to which, across
// exchanges, so an ask that would close a cycle (the target waits, directly or
// through others, on the caller) is refused instead of both sides waiting out
// their timeouts. The zero value is ready to use.
type waitGraph struct {
	mu    sync.Mutex
	edges map[string]map[string]int // caller -> target -> asks in flight
}

// begin records that caller waits on target, unless target already waits
// (transitively) on caller; it reports whether it did.
func (g *waitGraph) begin(caller, target string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.reachesLocked(target, caller, map[string]bool{}) {
		return false
	}
	if g.edges == nil {
		g.edges = make(map[string]map[string]int)
	}
	if g.edges[caller] == nil {
		g.edges[caller] = make(map[string]int)
	}
	g.edges[caller][target]++
	return true
}

// end removes one wait of caller on target.
func (g *waitGraph) end(caller, target string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.edges[caller][target] <= 1 {
		delete(g.edges[caller], target)
		if len(g.edges[caller]) == 0 {
			delete(g.edges, caller)
		}
		return
	}
	g.edges[caller][target]--
}

func (g *waitGraph) reachesLocked(from, to string, seen map[string]bool) bool {
	if from == to {
		return true
	}
	if seen[from] {
		return false
	}
	seen[from] = true
	for next := range g.edges[from] {
		if g.reachesLocked(next, to, seen) {
			return true
		}
	}
	return false
}

// askHeader heads an ask's message so the target knows who is waiting.
func askHeader(from sender) string {
	return "[Message from " + from.label() + " — " + from.name + " is waiting for your reply]"
}

// requestTimeoutFor is the request_timeout of agent's active model (the
// agents.defaults value when the model sets none); 0 when neither is set.
func (al *AgentLoop) requestTimeoutFor(agent *AgentInstance) time.Duration {
	cfg := al.GetConfig()
	if cfg == nil {
		return 0
	}
	model := agent.Model
	if len(agent.Candidates) > 0 {
		idx := al.getActiveModelIndex(agent, routing.BuildAgentMainSessionKey(agent.ID))
		if idx < 0 || idx >= len(agent.Candidates) {
			idx = 0
		}
		if c := agent.Candidates[idx]; c.Alias != "" {
			model = c.Alias
		} else if c.Model != "" {
			model = c.Model
		}
	}
	if mc, err := cfg.GetModelConfig(model); err == nil && mc != nil && mc.RequestTimeout > 0 {
		return time.Duration(mc.RequestTimeout) * time.Second
	}
	if cfg.Agents.Defaults.RequestTimeout > 0 {
		return time.Duration(cfg.Agents.Defaults.RequestTimeout) * time.Second
	}
	return 0
}

// secondsText is "1 second" or "N seconds".
func secondsText(n int) string {
	if n == 1 {
		return "1 second"
	}
	return fmt.Sprintf("%d seconds", n)
}

// waitSeconds renders a wait in whole seconds, rounded up.
func waitSeconds(d time.Duration) int {
	return int(math.Ceil(d.Seconds()))
}

// Ask implements tools.Messenger: agentID (a config or temporary agent) gets
// a normal turn with message in its one conversation, queued like any
// inbound message, and Ask returns its final reply. The turn runs one
// sub-agent level deeper than ctx; an ask from a turn already at
// max_subagent_depth is refused, as is one to an agent waiting in the same
// exchange. The reply is handed back here and never published to a channel.
func (al *AgentLoop) Ask(ctx context.Context, from, agentID, message string, wait time.Duration) (tools.AgentReply, error) {
	return al.ask(ctx, sender{name: strings.TrimSpace(from)}, agentID, message, wait, false)
}

// askStoppingTurn is Ask, except that the asked turn is cancelled (and its
// model call with it) as soon as the asker stops waiting: at the end of the
// wait or when ctx ends. A turn of a person is not cancelled; its request is
// withdrawn as for any ask.
func (al *AgentLoop) askStoppingTurn(ctx context.Context, from, agentID, message string, wait time.Duration) (tools.AgentReply, error) {
	return al.ask(ctx, sender{name: strings.TrimSpace(from)}, agentID, message, wait, true)
}

// ask is Ask from from; stopTurn is askStoppingTurn's.
func (al *AgentLoop) ask(ctx context.Context, from sender, agentID, message string, wait time.Duration, stopTurn bool) (tools.AgentReply, error) {
	target, err := al.askTarget(ctx, from, agentID, message, wait)
	if err != nil {
		return tools.AgentReply{}, err
	}
	name := target.DisplayName()
	depth := toolsagents.SpawnDepth(ctx)
	chain := tools.AskChain(ctx)
	// The running agent (last in the chain) waits on the target until Ask
	// returns; refused when the target already waits on it in another
	// exchange.
	if len(chain) > 0 {
		caller := chain[len(chain)-1]
		if !al.waits.begin(caller, target.ID) {
			return tools.AgentReply{}, fmt.Errorf("cannot ask %s: %w", name, tools.ErrAskLoop)
		}
		defer al.waits.end(caller, target.ID)
	}

	wait = al.askWaitFor(ctx, target, wait)
	timeoutReply := func() tools.AgentReply {
		return tools.AgentReply{
			Outcome: tools.OutcomeTimeout,
			Text:    fmt.Sprintf("%s did not reply within %s.", name, secondsText(waitSeconds(max(wait, 0)))),
		}
	}
	if wait <= 0 {
		// No time left to wait: the target is not given a turn nobody would
		// wait for.
		return timeoutReply(), nil
	}

	id := "ask-" + uuid.NewString()
	replies := al.asks.open(id, from.name, al.clk().Now().Add(wait), stopTurn)
	if err = al.publishAsk(ctx, id, from, target, message, chain, depth); err != nil {
		al.asks.close(id)
		return tools.AgentReply{}, fmt.Errorf("queue the message: %w", err)
	}
	fields := map[string]any{"ask_id": id, "agent_id": target.ID, "from": from.label(), "depth": depth + 1, "wait_s": waitSeconds(wait)}
	logger.InfoCF("agent", "Ask sent", fields)
	if slot := turnSlotFrom(ctx); slot != nil {
		slot.lend()
		defer slot.reclaim(ctx)
	}

	reply, answered, err := al.awaitAskReply(ctx, id, replies, wait)
	if err != nil {
		return tools.AgentReply{}, err
	}
	if !answered {
		logger.InfoCF("agent", "Ask timed out; a late reply is discarded", fields)
		return timeoutReply(), nil
	}
	fields["outcome"] = reply.Outcome
	logger.InfoCF("agent", "Ask answered", fields)
	return reply, nil
}

// askTarget checks an ask and returns the agent it goes to. An ask from a
// turn already at max_subagent_depth is refused, as is one to an agent
// waiting in the same exchange.
func (al *AgentLoop) askTarget(ctx context.Context, from sender, agentID, message string, wait time.Duration) (*AgentInstance, error) {
	switch {
	case from.name == "":
		return nil, errors.New("ask: the sender is required")
	case strings.TrimSpace(message) == "":
		return nil, errors.New("ask: the message is empty")
	case wait <= 0:
		return nil, errors.New("ask: the wait must be positive")
	}
	target, ok := al.GetRegistry().Get(agentID)
	if !ok || target == nil {
		return nil, fmt.Errorf("%w: %s", tools.ErrNoSuchAgent, agentID)
	}
	name := target.DisplayName()
	cfg := al.GetConfig()
	if !al.running.Load() || cfg == nil {
		return nil, errors.New("the service is not running")
	}

	maxDepth := cfg.Agents.Defaults.GetMaxSubagentDepth()
	if depth := toolsagents.SpawnDepth(ctx); depth >= maxDepth {
		return nil, fmt.Errorf("%w (%d): cannot ask %s from a turn %d level(s) deep",
			tools.ErrMaxDepth, maxDepth, name, depth)
	}
	if slices.Contains(tools.AskChain(ctx), target.ID) {
		return nil, fmt.Errorf("cannot ask %s: %w", name, tools.ErrAskLoop)
	}
	return target, nil
}

// askWaitFor is how long an ask waits: never longer than the target's
// request_timeout, nor past ctx's deadline.
func (al *AgentLoop) askWaitFor(ctx context.Context, target *AgentInstance, wait time.Duration) time.Duration {
	if rt := al.requestTimeoutFor(target); rt > 0 && rt < wait {
		wait = rt
	}
	if deadline, ok := ctx.Deadline(); ok {
		if left := al.clk().Until(deadline); left < wait {
			wait = left
		}
	}
	return wait
}

// publishAsk queues ask id for target as an inbound message on the ask
// channel. It requires one reply, runs one sub-agent level deeper than the
// asking turn, and carries the agents already waiting in the exchange.
func (al *AgentLoop) publishAsk(ctx context.Context, id string, from sender, target *AgentInstance, message string, chain []string, depth int) error {
	meta := map[string]string{
		metadataKeyPreresolvedAgentID: target.ID,
		bus.MetaReplyRequired:         "1",
		metadataKeyAskFrom:            from.name,
	}
	if len(chain) > 0 {
		meta[metadataKeyAskChain] = strings.Join(chain, ",")
	}
	meta = bus.SetSpawnDepth(meta, depth+1)
	msg := bus.InboundMessage{
		Channel:   constants.AgentMessageChannel,
		ChatID:    id,
		MessageID: id,
		SenderID:  from.label(),
		Content:   askHeader(from) + "\n" + message,
		Metadata:  meta,
		Internal:  true,
	}
	pubCtx, cancel := context.WithTimeout(ctx, publishTimeout)
	defer cancel()
	return al.bus.PublishInbound(pubCtx, msg)
}

// awaitAskReply waits up to wait for ask id's reply. It reports answered
// false when the wait ran out (ctx's deadline included); a ctx cancelled for
// any other reason is an error. Whenever it stops waiting, a reply that
// arrived meanwhile still wins.
func (al *AgentLoop) awaitAskReply(ctx context.Context, id string, replies <-chan tools.AgentReply, wait time.Duration) (tools.AgentReply, bool, error) {
	giveUp := func() (tools.AgentReply, bool) {
		al.asks.close(id)
		select {
		case reply := <-replies:
			return reply, true
		default:
			return tools.AgentReply{}, false
		}
	}
	timer := al.clk().NewTimer(wait)
	defer timer.Stop()
	select {
	case reply := <-replies:
		return reply, true, nil
	case <-timer.C():
	case <-ctx.Done():
		if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			if reply, ok := giveUp(); ok {
				return reply, true, nil
			}
			return tools.AgentReply{}, false, ctx.Err()
		}
	}
	reply, ok := giveUp()
	return reply, ok, nil
}

// deliverAskReply hands the final reply of an ask's turn (a message on
// constants.AgentMessageChannel) to the waiting asker. A reply nobody waits
// for any more is discarded with a log line.
// It reports whether the asker got it.
func (al *AgentLoop) deliverAskReply(msg bus.InboundMessage, text, outcome string) bool {
	if al.asks.deliver(msg.ChatID, tools.AgentReply{Text: text, Outcome: outcome}) {
		return true
	}
	logger.InfoCF("agent", "Late ask reply discarded: the asker stopped waiting",
		map[string]any{"ask_id": msg.ChatID, "agent_id": inboundMetadata(msg, metadataKeyPreresolvedAgentID), "outcome": outcome})
	return false
}
