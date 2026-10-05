// ClawEh
// License: MIT

package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/commands"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/providers"
	"github.com/PivotLLM/ClawEh/routing"
)

// askChannel is the internal channel core Ask (the agent_message tool, /ask
// and the forum) runs its turns on. A human agent takes work only from it.
// Matched by name: Ask is built in its own change and owns the constant.
const askChannel = "agent_message"

// Replies to the person in their own chat. One short sentence each.
const (
	nothingWaitingReply = "Nothing is waiting for your answer."
	timedOutReply       = "That request has already timed out."
	textOnlyReply       = "Please answer with text."
	cancelledReply      = "Cancelled."
)

// humanTurnGrace is added to a human agent's request timeout when that is
// longer than the turn budget, so the provider's own wait (an empty reply)
// ends the turn rather than the budget (an error). 30 s pending the
// maintainer's sign-off on the value.
const humanTurnGrace = 30 * time.Second

// errHumanCancelled is how a request ends when the person sends /cancel.
var errHumanCancelled = errors.New("the person cancelled the request")

// humanNotAskedError marks a turn routed to a human agent that is not an ask:
// it is dropped, never posted to the person. Its text is for the sender.
type humanNotAskedError struct{ label string }

func (e humanNotAskedError) Error() string { return e.label + " only answers questions from agents." }

// humanCancelledError is a request the person cancelled. Its text is for the
// sender.
type humanCancelledError struct{ label string }

func (e humanCancelledError) Error() string { return e.label + " cancelled the request." }
func (e humanCancelledError) Unwrap() error { return errHumanCancelled }

// humanDesk holds the requests waiting for a person's answer: at most one per
// human agent, the others queued behind it on the agent's slot.
type humanDesk struct {
	mu      sync.Mutex
	slots   map[string]chan struct{}
	pending map[string]*humanRequest
	// expired is when each agent's last request timed out unanswered, so a
	// late answer can be told so.
	expired map[string]time.Time
}

// humanRequest is one request posted to a person's chat.
type humanRequest struct {
	channel, chatID string
	answer          chan humanAnswer // buffered: one answer
}

// humanAnswer is what ended a request: the person's text, or /cancel.
type humanAnswer struct {
	text      string
	cancelled bool
}

func (d *humanDesk) slot(agentID string) chan struct{} {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.slots == nil {
		d.slots = make(map[string]chan struct{})
	}
	s := d.slots[agentID]
	if s == nil {
		s = make(chan struct{}, 1)
		d.slots[agentID] = s
	}
	return s
}

func (d *humanDesk) set(agentID string, req *humanRequest) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.pending == nil {
		d.pending = make(map[string]*humanRequest)
	}
	d.pending[agentID] = req
}

func (d *humanDesk) clear(agentID string, req *humanRequest) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.pending[agentID] == req {
		delete(d.pending, agentID)
	}
}

// waiting reports whether a request is waiting for agentID's answer.
func (d *humanDesk) waiting(agentID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.pending[agentID] != nil
}

// answer ends the request waiting on agentID with a when msg comes from the
// chat it was posted to, and reports whether it did. The first answer ends
// the wait; a later message finds nothing waiting.
func (d *humanDesk) answer(agentID string, msg bus.InboundMessage, a humanAnswer) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	req := d.pending[agentID]
	if req == nil || !sameChat(msg, req.channel, req.chatID) {
		return false
	}
	delete(d.pending, agentID)
	req.answer <- a
	return true
}

func (d *humanDesk) markExpired(agentID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.expired == nil {
		d.expired = make(map[string]time.Time)
	}
	d.expired[agentID] = time.Now()
}

// recentlyExpired reports whether agentID's last request timed out within
// window.
func (d *humanDesk) recentlyExpired(agentID string, window time.Duration) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	t, ok := d.expired[agentID]
	return ok && window > 0 && time.Since(t) < window
}

// inboundDismisser clears what a chat shows for an inbound message that gets
// no reply (channels.Manager.DismissInbound).
type inboundDismisser interface {
	DismissInbound(ctx context.Context, channel, chatID, messageID string)
}

// dismissTimeout bounds clearing a chat's indicators for an answer.
const dismissTimeout = 5 * time.Second

// sameChat reports whether msg was sent in channel:chatID, matched on the
// chat or on the routing peer (a thread's chat id carries more than the
// channel id a binding names).
func sameChat(msg bus.InboundMessage, channel, chatID string) bool {
	return msg.Channel == channel && chatID != "" && (msg.ChatID == chatID || msg.Peer.ID == chatID)
}

// turnSlot is a turn's hold on a max_concurrent_turns slot, put on the
// context of a turn addressed to a human agent only (processSessionMessage)
// and used by that turn's own goroutine only. A request to a person gives it
// up while it waits (askHuman), so a person taking an hour does not stall
// every other turn. This is self-contained on purpose: core Ask also lends
// the asking turn's slot while it waits for the target, and the two are to be
// reconciled when that branch is merged (one lending mechanism, not two).
type turnSlot struct {
	al   *AgentLoop
	held bool
}

func (s *turnSlot) acquire(ctx context.Context) bool {
	if s.al.acquireTurnSlot(ctx) {
		s.held = true
	}
	return s.held
}

func (s *turnSlot) release() {
	if s.held {
		s.held = false
		s.al.releaseTurnSlot()
	}
}

type turnSlotKey struct{}

func withTurnSlot(ctx context.Context, s *turnSlot) context.Context {
	return context.WithValue(ctx, turnSlotKey{}, s)
}

func turnSlotFrom(ctx context.Context) *turnSlot {
	if s, ok := ctx.Value(turnSlotKey{}).(*turnSlot); ok {
		return s
	}
	return nil
}

// askHuman posts request to the person's chat channel:chatID and waits for
// their next message there, at most timeout (0 = until ctx ends) from when it
// is posted. Requests to one human agent are answered one at a time: a second
// waits for the first to end. The turn's concurrency slot is given up while
// waiting. It returns context.DeadlineExceeded when timeout passes, ctx's
// error when ctx ends first, and errHumanCancelled on /cancel.
func (al *AgentLoop) askHuman(ctx context.Context, agentID, channel, chatID, request string, timeout time.Duration) (string, error) {
	if slot := turnSlotFrom(ctx); slot != nil {
		slot.release()
		defer slot.acquire(ctx) // re-taken for the rest of the turn; a no-op once ctx has ended
	}

	deskSlot := al.humans.slot(agentID)
	select {
	case deskSlot <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-deskSlot }()

	req := &humanRequest{channel: channel, chatID: chatID, answer: make(chan humanAnswer, 1)}
	al.humans.set(agentID, req)

	if err := al.bus.PublishOutbound(ctx, bus.OutboundMessage{Channel: channel, ChatID: chatID, Content: request}); err != nil {
		al.humans.clear(agentID, req)
		return "", fmt.Errorf("post the request to %s:%s: %w", channel, chatID, err)
	}
	logger.InfoCF("agent", "Request posted to a person; waiting for the answer",
		turnFields(ctx, map[string]any{"agent_id": agentID, "channel": channel, "chat_id": chatID, "timeout": timeout.String()}))

	waitCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	select {
	case a := <-req.answer:
		return answerResult(a)
	case <-waitCtx.Done():
	}
	// No answer can arrive once the request is cleared; one that arrived
	// while the wait was ending is delivered, not lost.
	al.humans.clear(agentID, req)
	select {
	case a := <-req.answer:
		return answerResult(a)
	default:
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	al.humans.markExpired(agentID)
	logger.InfoCF("agent", "No answer from the person before the request timed out",
		turnFields(ctx, map[string]any{"agent_id": agentID, "timeout": timeout.String()}))
	return "", context.DeadlineExceeded
}

func answerResult(a humanAnswer) (string, error) {
	if a.cancelled {
		return "", errHumanCancelled
	}
	return a.text, nil
}

// runHumanTurn is a turn of a human agent: the request goes to the person
// through the human provider and their answer is the reply. Only an ask
// (askChannel, reply required) reaches the person; anything else routed to a
// human agent is dropped. Nothing else of the ordinary turn runs: no context
// assembly, memory, compaction, vision or tools, so the conversation never
// reaches a model. An unanswered request is an empty reply.
func (al *AgentLoop) runHumanTurn(ctx context.Context, agent *AgentInstance, opts processOptions) (string, error) {
	if opts.Channel != askChannel || !opts.ReplyRequired {
		logger.InfoCF("agent", "Message to a person dropped: only questions from agents reach them",
			turnFields(ctx, map[string]any{"agent_id": agent.ID, "channel": opts.Channel, "sender_id": opts.SenderID}))
		al.stopTyping(opts.Channel, opts.ChatID)
		return "", humanNotAskedError{label: agentLabelForUser(agent)}
	}
	cfg := al.GetConfig()
	channel, chatID, _, ok := cfg.CronTarget(agent.ID)
	if !ok {
		return "", fmt.Errorf("%s has no chat to reach the person in", agentLabelForUser(agent))
	}
	if al.dispatcher == nil {
		return "", errors.New("no provider dispatcher")
	}
	p, err := al.dispatchProvider(agent, agent.HumanModel)
	if err != nil {
		return "", err
	}
	hp, ok := p.(*providers.HumanProvider)
	if !ok {
		return "", fmt.Errorf("model %q is not a person's model", agent.HumanModel)
	}
	relay := func(ctx context.Context, request string, timeout time.Duration) (string, error) {
		return al.askHuman(ctx, agent.ID, channel, chatID, request, timeout)
	}
	resp, err := hp.Chat(providers.WithHumanRelay(ctx, relay),
		[]providers.Message{{Role: "user", Content: opts.UserMessage}}, nil, agent.HumanModel, nil)
	if errors.Is(err, errHumanCancelled) {
		return "", humanCancelledError{label: agentLabelForUser(agent)}
	}
	if err != nil {
		return "", err
	}
	answer := strings.TrimSpace(resp.Content)
	if answer == "" {
		al.stopTyping(opts.Channel, opts.ChatID)
		opts.setOutcome(bus.OutcomeEmpty)
		return "", nil
	}
	if opts.SendResponse {
		if err := al.bus.PublishOutbound(ctx, bus.OutboundMessage{Channel: opts.Channel, ChatID: opts.ChatID, Content: answer}); err != nil {
			logger.WarnCF("agent", "Failed to publish response",
				map[string]any{"error": err.Error(), "channel": opts.Channel, "session": opts.SessionKey})
		}
	}
	if opts.IterationsOut != nil {
		*opts.IterationsOut = 1
	}
	return answer, nil
}

// agentLabelForUser names an agent in a message: its name, else its id.
func agentLabelForUser(agent *AgentInstance) string {
	if agent.Name != "" {
		return agent.Name
	}
	return agent.ID
}

// humanChat is a person's chat: the human agent it belongs to in the
// configuration, and its running instance (nil when the agent is not run:
// disabled, or set aside for breaking the human-agent rules).
type humanChat struct {
	id, label string
	agent     *AgentInstance
}

// humanChatOwner returns the human agent whose chat msg was written in by a
// person. The chats come from the configuration, so the chat of a human agent
// that is not running is still known and never reaches another agent.
func (al *AgentLoop) humanChatOwner(msg bus.InboundMessage) (humanChat, bool) {
	if !fromPerson(msg) {
		return humanChat{}, false
	}
	cfg, registry := al.GetConfig(), al.GetRegistry()
	if cfg == nil || registry == nil {
		return humanChat{}, false
	}
	for i := range cfg.Agents.List {
		ac := &cfg.Agents.List[i]
		if _, human := cfg.HumanModelOf(ac); !human {
			continue
		}
		channel, chatID, _, found := cfg.CronTarget(ac.ID)
		if !found || !sameChat(msg, channel, chatID) {
			continue
		}
		hc := humanChat{id: routing.NormalizeAgentID(ac.ID), label: humanLabel(ac)}
		if inst, ok := registry.GetConfigured(hc.id); ok && inst.HumanModel != "" {
			hc.agent = inst
		}
		return hc, true
	}
	return humanChat{}, false
}

func humanLabel(ac *config.AgentConfig) string {
	if ac.Name != "" {
		return ac.Name
	}
	return ac.ID
}

// fromPerson reports whether msg was written by someone, as opposed to
// published by claw itself into a chat (a scheduled job, a webhook, a session
// reset, an async result, a restart replay, a mount notice, a callback).
func fromPerson(msg bus.InboundMessage) bool {
	switch msg.SenderID {
	case "cron", "webhook", "system", "recovery", "mount-notify":
		return false
	}
	return !strings.HasPrefix(msg.SenderID, "async:") && !strings.HasPrefix(msg.SenderID, "callback")
}

// humanCommand reports whether text is a command in a person's chat: only
// "/" starts one there, so an answer beginning with another trigger is an
// answer.
func humanCommand(text string) bool {
	return strings.HasPrefix(strings.TrimSpace(text), "/")
}

// takeHumanAnswer hands msg to the request waiting for it when msg is the
// person's text answer, and reports whether it did. dispatchInbound calls it
// for each inbound message in arrival order, before any goroutine is started,
// so the person's messages are taken as answers one at a time and in order.
// It is the only place an answer is taken.
func (al *AgentLoop) takeHumanAnswer(ctx context.Context, msg bus.InboundMessage) bool {
	hc, ok := al.humanChatOwner(msg)
	if !ok || hc.agent == nil || humanCommand(msg.Content) || strings.TrimSpace(msg.Content) == "" {
		return false
	}
	if !al.humans.answer(hc.id, msg, humanAnswer{text: msg.Content}) {
		return false
	}
	logger.InfoCF("agent", "Answer received from a person",
		map[string]any{"agent_id": hc.id, "channel": msg.Channel, "chat_id": msg.ChatID})
	// The answer gets no reply of its own: clear what the chat shows for it,
	// off the inbound dispatcher (the channel may be slow to answer).
	if d := al.dismisser; d != nil {
		go func() {
			dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dismissTimeout)
			defer cancel()
			d.DismissInbound(dctx, msg.Channel, msg.ChatID, msg.MessageID)
		}()
	}
	return true
}

// handleHumanChat handles a message a person wrote in their human agent's
// chat, and reports whether it did:
//   - the agent is not running: the person is told so;
//   - a command: /cancel ends the waiting request; any other command runs at
//     once while a request waits (the waiting turn holds the agent's
//     session), else takes the ordinary command path;
//   - an attachment alone does not answer: the person is asked for text;
//   - text reaches here only when it found nothing waiting (dispatchInbound
//     takes answers): the person is told nothing is waiting, or that the
//     request they answer has timed out.
//
// No turn ever starts for the person's message.
func (al *AgentLoop) handleHumanChat(ctx context.Context, msg bus.InboundMessage) bool {
	hc, ok := al.humanChatOwner(msg)
	if !ok {
		return false
	}
	if hc.agent == nil {
		al.replyInHumanChat(ctx, msg, hc.label+" is not running.")
		return true
	}
	if humanCommand(msg.Content) {
		if !al.humans.waiting(hc.id) {
			return false
		}
		if name, _ := commands.ParseCommandName(msg.Content); name == "cancel" {
			if al.humans.answer(hc.id, msg, humanAnswer{cancelled: true}) {
				al.replyInHumanChat(ctx, msg, cancelledReply)
				return true
			}
		}
		al.runTurn(ctx, ctx, msg)
		return true
	}
	if strings.TrimSpace(msg.Content) == "" {
		reply := nothingWaitingReply
		if al.humans.waiting(hc.id) {
			reply = textOnlyReply
		}
		al.replyInHumanChat(ctx, msg, reply)
		return true
	}
	window := time.Duration(al.GetConfig().HumanRequestTimeout(hc.id)) * time.Second
	if al.humans.recentlyExpired(hc.id, window) {
		al.replyInHumanChat(ctx, msg, timedOutReply)
	} else {
		al.replyInHumanChat(ctx, msg, nothingWaitingReply)
	}
	return true
}

func (al *AgentLoop) replyInHumanChat(ctx context.Context, msg bus.InboundMessage, text string) {
	if err := al.bus.PublishOutbound(ctx, bus.OutboundMessage{
		Channel:           msg.Channel,
		ChatID:            msg.ChatID,
		Content:           text,
		OriginalMessageID: msg.MessageID,
		Outcome:           bus.OutcomeOK,
	}); err != nil {
		logger.WarnCF("agent", "Failed to publish reply", map[string]any{
			"channel": msg.Channel, "chat_id": msg.ChatID, "error": err.Error(),
		})
	}
}

// humanTarget returns the human agent msg is addressed to, or nil, resolved
// as processMessage will route it. A preresolved agent that is gone is left to
// processMessage to report.
func (al *AgentLoop) humanTarget(msg bus.InboundMessage) *AgentInstance {
	registry := al.GetRegistry()
	if registry == nil || msg.Channel == "system" {
		return nil
	}
	var agent *AgentInstance
	if id := inboundMetadata(msg, metadataKeyPreresolvedAgentID); id != "" {
		agent, _ = registry.Get(routing.NormalizeAgentID(id))
	} else if _, routed, err := al.resolveMessageRoute(msg); err == nil {
		agent = routed
	}
	if agent == nil || agent.HumanModel == "" {
		return nil
	}
	return agent
}

// humanTurnBudget is the turn budget for msg: base, or for a request to a
// person the time their model waits for an answer plus humanTurnGrace when
// that is longer.
func (al *AgentLoop) humanTurnBudget(msg bus.InboundMessage, base time.Duration) time.Duration {
	agent := al.humanTarget(msg)
	if agent == nil {
		return base
	}
	wait := time.Duration(al.GetConfig().HumanRequestTimeout(agent.ID)) * time.Second
	if wait+humanTurnGrace > base {
		return wait + humanTurnGrace
	}
	return base
}
