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
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/constants"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/routing"
	"github.com/PivotLLM/ClawEh/tools"
	toolsagents "github.com/PivotLLM/ClawEh/tools/agents"
)

// AgentLoop is the core Messenger: agent_message, /ask, /whisper and the
// forum all send through it.
var _ tools.Messenger = (*AgentLoop)(nil)

// metadataKeyAskChain carries, on an ask's inbound message, the agents
// already waiting in the exchange (comma-separated ids), so the asked agent
// cannot ask any of them back: each would wait on the other until the
// timeout.
const metadataKeyAskChain = "ask_chain"

// metadataKeyAskFrom carries, on an ask's inbound message, the asker's name,
// so a person whose answer arrives after the asker stopped waiting can be
// told who no longer needs it.
const metadataKeyAskFrom = "ask_from"

// agentMessageToolName is the tool a whisper's recipient answers with.
const agentMessageToolName = "agent_message"

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

// whisper is one held message.
type whisper struct {
	fromID string // the sending agent's id; empty for a person
	from   string
	note   string // how a person sent it, e.g. "a person, via /whisper on telegram"; empty for an agent
	text   string
}

// sender is who an ask or whisper is from: an agent's or a person's name,
// and for a person how they sent it, so the recipient cannot take them for
// an agent of the same name.
type sender struct {
	id   string // the agent's id; empty for a person
	name string
	note string
}

// label is the sender as the recipient sees it: "Alice", or
// "Alice (a person, via /ask on telegram)".
func (s sender) label() string {
	if s.note == "" {
		return s.name
	}
	return s.name + " (" + s.note + ")"
}

// personNote describes a person sending command on channel.
func personNote(command, channel string) string {
	return "a person, via /" + command + " on " + channel
}

// whisperStore holds whispers per agent until its next message. It is in
// memory: whispers not yet delivered are lost on restart. The zero value is
// ready to use.
type whisperStore struct {
	mu   sync.Mutex
	held map[string][]whisper
}

func (s *whisperStore) add(agentID string, w whisper) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.held == nil {
		s.held = make(map[string][]whisper)
	}
	s.held[agentID] = append(s.held[agentID], w)
}

// take removes and returns agentID's held whispers, oldest first.
func (s *whisperStore) take(agentID string) []whisper {
	s.mu.Lock()
	defer s.mu.Unlock()
	w := s.held[agentID]
	delete(s.held, agentID)
	return w
}

// drop forgets agentID's whispers (a deleted temporary agent).
func (s *whisperStore) drop(agentID string) {
	s.take(agentID)
}

// prune forgets the whispers of every agent exists reports gone (an agent
// removed from the configuration).
func (s *whisperStore) prune(exists func(agentID string) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id := range s.held {
		if !exists(id) {
			delete(s.held, id)
		}
	}
}

// askHeader heads an ask's message so the target knows who is waiting.
func askHeader(from sender) string {
	return "[Message from " + from.label() + " — " + from.name + " is waiting for your reply]"
}

// whisperBlock renders held whispers for the start of the next message. The
// hint on answering is given only for a whisper canAnswer (nil: none) says
// the recipient can answer.
func whisperBlock(ws []whisper, canAnswer func(w whisper) bool) string {
	lines := make([]string, 0, len(ws))
	for _, w := range ws {
		head := "[Private whisper from " + sender{name: w.from, note: w.note}.label() + " — no reply expected."
		if canAnswer != nil && canAnswer(w) {
			head += " To answer privately, use " + agentMessageToolName + " with wait_seconds 0."
		}
		lines = append(lines, head+"] "+w.text)
	}
	return strings.Join(lines, "\n")
}

// prependWhispers adds agent's held whispers to the start of message,
// removing them so each is delivered exactly once.
func (al *AgentLoop) prependWhispers(agent *AgentInstance, message string) string {
	ws := al.whispers.take(agent.ID)
	if len(ws) == 0 {
		return message
	}
	logger.InfoCF("agent", "Delivering whispers",
		map[string]any{"agent_id": agent.ID, "count": len(ws)})
	return whisperBlock(ws, al.canAnswerWhisper(agent)) + "\n\n" + message
}

// canAnswerWhisper returns whether agent can answer a whisper privately: it
// has agent_message and the whisper's sender is an agent in its
// subagents.allow_agents. A person's whisper cannot be answered that way.
func (al *AgentLoop) canAnswerWhisper(agent *AgentInstance) func(w whisper) bool {
	if _, ok := agent.Tools.Get(agentMessageToolName); !ok {
		return nil
	}
	return func(w whisper) bool {
		return w.fromID != "" && newAgentServices(al, agent.ID).CanTarget(w.fromID)
	}
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
	switch {
	case from.name == "":
		return tools.AgentReply{}, errors.New("ask: the sender is required")
	case strings.TrimSpace(message) == "":
		return tools.AgentReply{}, errors.New("ask: the message is empty")
	case wait <= 0:
		return tools.AgentReply{}, errors.New("ask: the wait must be positive")
	}
	target, ok := al.GetRegistry().Get(agentID)
	if !ok || target == nil {
		return tools.AgentReply{}, fmt.Errorf("%w: %s", tools.ErrNoSuchAgent, agentID)
	}
	name := target.DisplayName()
	cfg := al.GetConfig()
	if !al.running.Load() || cfg == nil {
		return tools.AgentReply{}, errors.New("the service is not running")
	}

	maxDepth := cfg.Agents.Defaults.GetMaxSubagentDepth()
	depth := toolsagents.SpawnDepth(ctx)
	if depth >= maxDepth {
		return tools.AgentReply{}, fmt.Errorf("%w (%d): cannot ask %s from a turn %d level(s) deep",
			tools.ErrMaxDepth, maxDepth, name, depth)
	}
	chain := tools.AskChain(ctx)
	if slices.Contains(chain, target.ID) {
		return tools.AgentReply{}, fmt.Errorf("cannot ask %s: %w", name, tools.ErrAskLoop)
	}
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

	if rt := al.requestTimeoutFor(target); rt > 0 && rt < wait {
		wait = rt
	}
	if deadline, ok := ctx.Deadline(); ok {
		if left := time.Until(deadline); left < wait {
			wait = left
		}
	}
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
	replies := al.asks.open(id, from.name, time.Now().Add(wait), stopTurn)
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
	pubCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	err := al.bus.PublishInbound(pubCtx, msg)
	cancel()
	if err != nil {
		al.asks.close(id)
		return tools.AgentReply{}, fmt.Errorf("queue the message: %w", err)
	}
	fields := map[string]any{"ask_id": id, "agent_id": target.ID, "from": from.label(), "depth": depth + 1, "wait_s": waitSeconds(wait)}
	logger.InfoCF("agent", "Ask sent", fields)
	if slot := turnSlotFrom(ctx); slot != nil {
		slot.lend()
		defer slot.reclaim(ctx)
	}

	answered := func(reply tools.AgentReply) tools.AgentReply {
		fields["outcome"] = reply.Outcome
		logger.InfoCF("agent", "Ask answered", fields)
		return reply
	}
	// giveUp stops waiting; a reply that arrived meanwhile still wins.
	giveUp := func() (tools.AgentReply, bool) {
		al.asks.close(id)
		select {
		case reply := <-replies:
			return answered(reply), true
		default:
			return tools.AgentReply{}, false
		}
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case reply := <-replies:
		return answered(reply), nil
	case <-timer.C:
	case <-ctx.Done():
		if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			if reply, ok := giveUp(); ok {
				return reply, nil
			}
			return tools.AgentReply{}, ctx.Err()
		}
	}
	if reply, ok := giveUp(); ok {
		return reply, nil
	}
	logger.InfoCF("agent", "Ask timed out; a late reply is discarded", fields)
	return timeoutReply(), nil
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

// Whisper implements tools.Messenger: message is held for agentID and added,
// marked private, to the start of the next message it receives, whatever its
// source. It never starts a turn.
func (al *AgentLoop) Whisper(ctx context.Context, fromID, from, agentID, message string) error {
	return al.whisper(ctx, sender{id: strings.TrimSpace(fromID), name: strings.TrimSpace(from)}, agentID, message)
}

// whisper is Whisper from from.
func (al *AgentLoop) whisper(_ context.Context, from sender, agentID, message string) error {
	if from.name == "" {
		return errors.New("whisper: the sender is required")
	}
	if strings.TrimSpace(message) == "" {
		return errors.New("whisper: the message is empty")
	}
	target, ok := al.GetRegistry().Get(agentID)
	if !ok || target == nil {
		return fmt.Errorf("%w: %s", tools.ErrNoSuchAgent, agentID)
	}
	al.whispers.add(target.ID, whisper{fromID: from.id, from: from.name, note: from.note, text: message})
	logger.InfoCF("agent", "Whisper held for the agent's next message",
		map[string]any{"agent_id": target.ID, "from": from.label()})
	return nil
}

// webUIChannel is the WebUI chat's channel name. Its sender has no name of
// its own (only an internal id), so commandSender names it webUISender.
const (
	webUIChannel = "webui"
	webUISender  = "the WebUI user"
)

// commandSender names the sender of a chat message for the agent it asks or
// whispers to.
func commandSender(msg bus.InboundMessage) string {
	if msg.Channel == webUIChannel {
		return webUISender
	}
	if label := senderLabel(msg.Sender); label != "" {
		return label
	}
	if msg.SenderID != "" {
		return msg.SenderID
	}
	return msg.Channel
}

// senderMayReach reports whether the sender of msg may talk to agentID: msg
// is routed to it, or a binding matching msg's channel, account and chat
// routes to it or lets it be mentioned.
func (al *AgentLoop) senderMayReach(msg bus.InboundMessage, agentID string) bool {
	if route, _, err := al.resolveMessageRoute(msg); err == nil && route.AgentID == agentID {
		return true
	}
	cfg := al.GetConfig()
	if cfg == nil {
		return false
	}
	return routing.NewRouteResolver(cfg).Reaches(routing.RouteInput{
		Channel:    msg.Channel,
		AccountID:  inboundMetadata(msg, metadataKeyAccountID),
		Peer:       extractPeer(msg),
		ParentPeer: extractParentPeer(msg),
		GuildID:    inboundMetadata(msg, metadataKeyGuildID),
		TeamID:     inboundMetadata(msg, metadataKeyTeamID),
	}, agentID)
}

// commandTarget resolves the agent a /ask or /whisper names (by id or name,
// config agents only) and checks the sender may talk to it. It returns the
// agent, or the reply to give instead.
func (al *AgentLoop) commandTarget(command string, msg bus.InboundMessage, ref string) (*AgentInstance, string) {
	cfg := al.GetConfig()
	var target *AgentInstance
	if cfg != nil {
		if ac := cfg.FindAgent(ref); ac != nil {
			target, _ = al.GetRegistry().GetConfigured(ac.ID)
		}
	}
	if target == nil {
		return nil, fmt.Sprintf("There is no agent named %s.", ref)
	}
	if !al.senderMayReach(msg, target.ID) {
		logger.WarnCF("agent", "Agent message refused: the sender may not talk to the agent",
			map[string]any{"command": command, "agent_id": target.ID, "channel": msg.Channel, "sender_id": msg.SenderID})
		return nil, fmt.Sprintf("You don't have permission to /%s %s", command, target.DisplayName())
	}
	return target, ""
}

// commandWhisper runs /whisper <agent> <text> for msg's sender.
func (al *AgentLoop) commandWhisper(ctx context.Context, msg bus.InboundMessage, ref, text string) string {
	if tools.AgentMessageTooLong(text) {
		return tools.AgentMessageLimitText()
	}
	target, refusal := al.commandTarget("whisper", msg, ref)
	if target == nil {
		return refusal
	}
	name := target.DisplayName()
	from := sender{name: commandSender(msg), note: personNote("whisper", msg.Channel)}
	if err := al.whisper(ctx, from, target.ID, text); err != nil {
		return fmt.Sprintf("Could not whisper to %s: %v", name, err)
	}
	return "Whispered to " + name + "."
}

// commandAsk runs /ask <agent> <text> for msg's sender. The ask runs in the
// background, waiting up to the agent's request_timeout (the turn timeout
// when none is set), so the chat is not held; the answer is posted to the
// chat as "<agent>: <reply>". It returns the reply to give now: empty when
// the ask was sent.
func (al *AgentLoop) commandAsk(ctx context.Context, msg bus.InboundMessage, ref, text string) string {
	if tools.AgentMessageTooLong(text) {
		return tools.AgentMessageLimitText()
	}
	target, refusal := al.commandTarget("ask", msg, ref)
	if target == nil {
		return refusal
	}
	wait := al.requestTimeoutFor(target)
	if wait <= 0 {
		if cfg := al.GetConfig(); cfg != nil {
			wait = cfg.Agents.Defaults.GetTurnTimeout()
		} else {
			wait = config.DefaultTurnTimeout
		}
	}
	from := sender{name: commandSender(msg), note: personNote("ask", msg.Channel)}
	// The ask outlives the command's turn, so it is not cancelled with it, but
	// it stops when the service does. It keeps the turn's values: a person's
	// message carries no sub-agent depth and nobody waits on it, so the ask
	// starts at depth 0 with an empty chain, and it is marked remote when the
	// chat is.
	// It holds no turn slot: the command's turn ends before it does.
	askCtx, cancel := context.WithCancel(withTurnSlot(context.WithoutCancel(ctx), nil))
	stop := func() bool { return false }
	if runCtx := al.runContext(); runCtx != nil {
		stop = context.AfterFunc(runCtx, cancel) //nolint:contextcheck // the run context only ends the ask; its values come from the command's turn
	}
	go func() {
		defer cancel()
		defer stop()
		reply, err := al.ask(askCtx, from, target.ID, text, wait, false)
		if errors.Is(err, context.Canceled) {
			logger.InfoCF("agent", "/ask abandoned: the service is stopping",
				map[string]any{"agent_id": target.ID, "channel": msg.Channel, "chat_id": msg.ChatID})
			return
		}
		content := commandAskReply(target.DisplayName(), reply, err)
		pubCtx, cancel := context.WithTimeout(askCtx, 5*time.Second)
		defer cancel()
		if pubErr := al.bus.PublishOutbound(pubCtx, bus.OutboundMessage{
			Channel:           msg.Channel,
			ChatID:            msg.ChatID,
			Content:           content,
			OriginalMessageID: msg.MessageID,
		}); pubErr != nil {
			logger.WarnCF("agent", "Failed to post the /ask reply",
				map[string]any{"channel": msg.Channel, "chat_id": msg.ChatID, "error": pubErr.Error()})
		}
	}()
	return ""
}

// commandAskReply renders an ask's result for the chat that sent /ask.
func commandAskReply(name string, reply tools.AgentReply, err error) string {
	switch {
	case errors.Is(err, tools.ErrMaxDepth) || errors.Is(err, tools.ErrAskLoop):
		return err.Error()
	case err != nil:
		return fmt.Sprintf("Could not ask %s: %v", name, err)
	case reply.Outcome == tools.OutcomeTimeout || reply.Outcome == tools.OutcomePersonCancelled ||
		reply.Outcome == tools.OutcomePersonUnreachable:
		return reply.Text
	case strings.TrimSpace(reply.Text) == "":
		return name + " gave no reply."
	default:
		return name + ": " + reply.Text
	}
}
