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
	"github.com/PivotLLM/ClawEh/channels"
	"github.com/PivotLLM/ClawEh/commands"
	"github.com/PivotLLM/ClawEh/constants"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/providers"
	"github.com/PivotLLM/ClawEh/routing"
)

// Replies to the person in their own chat. One short sentence each.
const (
	nothingWaitingReply = "Nothing is waiting for your answer."
	timedOutReply       = "That request has already timed out."
	withdrawnReply      = "That request was withdrawn."
	textOnlyReply       = "Please answer with text."
	cancelledReply      = "Cancelled."
)

// humanTurnGrace is added to a human agent's request timeout when that is
// longer than the turn budget, so the provider's own wait (an empty reply)
// ends the turn rather than the budget (an error). 30 s leaves the request's
// timeout room to fire, the request to be withdrawn in the chat and the reply
// to be delivered, without holding a turn much longer than the person was
// given.
const humanTurnGrace = 30 * time.Second

// errHumanCancelled is how a request ends when the person sends /cancel.
var errHumanCancelled = errors.New("the person cancelled the request")

// errAskerStopped ends a request to a person whose asker stopped waiting (it
// gave up, or its deadline passed): no reply is handed back, the asker has
// its own outcome.
var errAskerStopped = errors.New("the asker stopped waiting")

// humanNotAskedError marks a turn routed to a human agent that is not an ask:
// it is dropped, never posted to the person. Its text is for the sender.
type humanNotAskedError struct{ label string }

func (e humanNotAskedError) Error() string { return e.label + " only answers questions from agents." }

// errChatUnreachable ends a request that could not be posted to the person's
// chat (the channel gave up on it): the person never saw it, so the asker is
// told at once rather than after the timeout.
var errChatUnreachable = errors.New("the request could not be posted to the person's chat")

// humanUnreachableError is a request that never reached the person's chat.
// Its text is for the sender and says why, from the channel's reason in
// cause.
type humanUnreachableError struct {
	label string
	cause error
}

func (e humanUnreachableError) Error() string {
	switch {
	case errors.Is(e.cause, channels.ErrUnknownChannel):
		return e.label + "'s chat is not set up."
	case errors.Is(e.cause, channels.ErrNotRunning):
		return e.label + "'s chat is unavailable."
	case errors.Is(e.cause, channels.ErrRecipientOffline):
		return e.label + "'s device is offline."
	case errors.Is(e.cause, channels.ErrRecipientNotFound):
		return e.label + "'s chat can't be reached."
	}
	return "Couldn't reach " + e.label + "'s chat."
}
func (e humanUnreachableError) Unwrap() error { return errChatUnreachable }

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
	// expired is how and when each agent's last request ended unanswered
	// (timed out or withdrawn), so a late answer can be told so.
	expired map[string]expiry
}

// expiry is how a request ended unanswered: the reply a late answer gets.
type expiry struct {
	at    time.Time
	reply string
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

// markExpired records that agentID's request ended unanswered; reply is what
// a late answer is told.
func (d *humanDesk) markExpired(agentID, reply string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.expired == nil {
		d.expired = make(map[string]expiry)
	}
	d.expired[agentID] = expiry{at: time.Now(), reply: reply}
}

// recentlyExpired returns what a late answer is told when agentID's last
// request ended unanswered within window.
func (d *humanDesk) recentlyExpired(agentID string, window time.Duration) (string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	e, ok := d.expired[agentID]
	if !ok || window <= 0 || time.Since(e.at) >= window {
		return "", false
	}
	return e.reply, true
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

// askHuman posts request to the person's chat channel:chatID and waits for
// their next message there, at most timeout (0 = until ctx ends) from when it
// is posted. Requests to one human agent are answered one at a time: a second
// waits for the first to end. The turn's concurrency slot is lent while
// waiting.
//
// ask, when the request answers an ask, is its asker: the wait never outlasts
// the asker's deadline, and a request whose asker stops waiting first is
// withdrawn in the person's chat with one line. Every answer the person gives
// is either returned or met with a line saying the request ended.
//
// It returns context.DeadlineExceeded when timeout passes, errAskerStopped
// when the asker's deadline passes or it stops waiting, ctx's error when ctx
// ends first, errHumanCancelled on /cancel, and errChatUnreachable as soon as
// the channel reports that the request could not be posted.
func (al *AgentLoop) askHuman(ctx context.Context, agentID, channel, chatID, request string, timeout time.Duration, ask *askWait) (string, error) {
	if slot := turnSlotFrom(ctx); slot != nil {
		slot.lend()
		defer slot.reclaim(ctx)
	}
	var gone <-chan struct{}
	if ask != nil {
		gone = ask.gone
	}

	deskSlot := al.humans.slot(agentID)
	select {
	case deskSlot <- struct{}{}:
	case <-gone:
		return "", errAskerStopped // never posted
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-deskSlot }()

	// The person's window never outlasts the asker.
	cappedByAsker := false
	if ask != nil {
		left := time.Until(ask.deadline)
		if left <= 0 {
			return "", errAskerStopped // never posted
		}
		if timeout <= 0 || left < timeout {
			timeout, cappedByAsker = left, true
		}
	}

	// Whispers held for the person open the request they see, once. A
	// person has no tools, so no hint on answering.
	if ws := al.whispers.take(agentID); len(ws) > 0 {
		logger.InfoCF("agent", "Delivering whispers", map[string]any{"agent_id": agentID, "count": len(ws)})
		request = whisperBlock(ws, nil) + "\n\n" + request
	}
	req := &humanRequest{channel: channel, chatID: chatID, answer: make(chan humanAnswer, 1)}
	al.humans.set(agentID, req)

	// The channel reports whether the request reached the chat; a failure
	// ends the wait at once, a success is logged.
	fields := turnFields(ctx, map[string]any{"agent_id": agentID, "channel": channel, "chat_id": chatID, "timeout": timeout.String()})
	undelivered := make(chan error, 1)
	onDelivery := func(err error) {
		if err == nil {
			logger.InfoCF("agent", "Request delivered to a person; waiting for the answer", fields)
			return
		}
		select {
		case undelivered <- err:
		default:
		}
	}
	logger.InfoCF("agent", "Posting request to a person", fields)
	if err := al.bus.PublishOutbound(ctx, bus.OutboundMessage{Channel: channel, ChatID: chatID, Content: request, OnDelivery: onDelivery}); err != nil {
		al.humans.clear(agentID, req)
		return "", fmt.Errorf("post the request to %s:%s: %w", channel, chatID, err)
	}
	waitCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	withdrawn := false
	select {
	case a := <-req.answer:
		return answerResult(a)
	case err := <-undelivered:
		al.humans.clear(agentID, req)
		select {
		case a := <-req.answer: // answered after all (the failure was partial)
			return answerResult(a)
		default:
		}
		logger.WarnCF("agent", "Request to a person could not be posted to their chat",
			turnFields(ctx, map[string]any{"agent_id": agentID, "channel": channel, "chat_id": chatID, "error": err.Error()}))
		return "", fmt.Errorf("%w: %s:%s: %w", errChatUnreachable, channel, chatID, err)
	case <-waitCtx.Done():
	case <-gone:
		// An asker that stopped at its deadline is the request timing out,
		// not a withdrawal (both may be ready at once).
		withdrawn = ask == nil || time.Now().Before(ask.deadline)
	}
	// No answer can arrive once the request is cleared; one that arrived
	// while the wait was ending is returned, not lost.
	al.humans.clear(agentID, req)
	select {
	case a := <-req.answer:
		return answerResult(a)
	default:
	}
	if withdrawn || ctx.Err() != nil {
		// The asker stopped waiting, or the turn ended (cancel, shutdown):
		// the person is told the request is withdrawn.
		from := ""
		if ask != nil {
			from = ask.from
		}
		al.humans.markExpired(agentID, withdrawnReply)
		al.tellNoLongerNeeded(ctx, agentID, channel, chatID, from)
		if withdrawn {
			return "", errAskerStopped
		}
		return "", ctx.Err()
	}
	al.humans.markExpired(agentID, timedOutReply)
	logger.InfoCF("agent", "No answer from the person before the request timed out",
		turnFields(ctx, map[string]any{"agent_id": agentID, "timeout": timeout.String()}))
	if cappedByAsker {
		return "", errAskerStopped
	}
	return "", context.DeadlineExceeded
}

// noLongerNeeded is the line that withdraws a request in the person's chat.
func noLongerNeeded(from string) string {
	if strings.TrimSpace(from) == "" {
		return "That request is no longer needed."
	}
	return from + " no longer needs an answer to that request."
}

// tellNoLongerNeeded posts noLongerNeeded(from) to the person's chat, even
// when ctx has ended.
func (al *AgentLoop) tellNoLongerNeeded(ctx context.Context, agentID, channel, chatID, from string) {
	pubCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), publishTimeout)
	defer cancel()
	if err := al.bus.PublishOutbound(pubCtx, bus.OutboundMessage{Channel: channel, ChatID: chatID, Content: noLongerNeeded(from)}); err != nil {
		logger.WarnCF("agent", "Failed to tell the person a request is no longer needed",
			map[string]any{"agent_id": agentID, "channel": channel, "chat_id": chatID, "error": err.Error()})
		return
	}
	logger.InfoCF("agent", "Request to a person withdrawn: the asker no longer waits",
		map[string]any{"agent_id": agentID, "channel": channel, "chat_id": chatID})
}

// humanAnswerUnused tells the person whose answer to msg (an ask) reached
// nobody, because the asker stopped waiting as it arrived, that it is no
// longer needed. A no-op when msg was not addressed to a human agent.
func (al *AgentLoop) humanAnswerUnused(ctx context.Context, msg bus.InboundMessage) {
	agent := al.humanTarget(msg)
	if agent == nil {
		return
	}
	channel, chatID, _, ok := al.GetConfig().CronTarget(agent.ID)
	if !ok {
		return
	}
	al.tellNoLongerNeeded(ctx, agent.ID, channel, chatID, inboundMetadata(msg, metadataKeyAskFrom))
}

func answerResult(a humanAnswer) (string, error) {
	if a.cancelled {
		return "", errHumanCancelled
	}
	return a.text, nil
}

// runHumanTurn is a turn of a human agent: the request goes to the person
// through the human provider and their answer is the reply. Only an ask
// (constants.AgentMessageChannel, reply required) reaches the person; anything else routed to a
// human agent is dropped. Nothing else of the ordinary turn runs: no context
// assembly, memory, compaction, vision or tools, so the conversation never
// reaches a model. An unanswered request is an empty reply.
func (al *AgentLoop) runHumanTurn(ctx context.Context, agent *AgentInstance, opts processOptions) (string, error) {
	if opts.Channel != constants.AgentMessageChannel || !opts.ReplyRequired {
		logger.InfoCF("agent", "Message to a person dropped: only questions from agents reach them",
			turnFields(ctx, map[string]any{"agent_id": agent.ID, "channel": opts.Channel, "sender_id": opts.SenderID}))
		al.stopTyping(opts.Channel, opts.ChatID)
		return "", humanNotAskedError{label: agent.DisplayName()}
	}
	// The asker, whose wait bounds the person's.
	w, waiting := al.asks.wait(opts.ChatID)
	if !waiting {
		return "", errAskerStopped
	}
	ask := &w
	cfg := al.GetConfig()
	channel, chatID, _, ok := cfg.CronTarget(agent.ID)
	if !ok {
		return "", fmt.Errorf("%s has no chat to reach the person in", agent.DisplayName())
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
		return al.askHuman(ctx, agent.ID, channel, chatID, request, timeout, ask)
	}
	resp, err := hp.Chat(providers.WithHumanRelay(ctx, relay),
		[]providers.Message{{Role: "user", Content: opts.UserMessage}}, nil, agent.HumanModel, nil)
	if errors.Is(err, errHumanCancelled) {
		return "", humanCancelledError{label: agent.DisplayName()}
	}
	if errors.Is(err, errChatUnreachable) {
		return "", humanUnreachableError{label: agent.DisplayName(), cause: err}
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
		hc := humanChat{id: ac.ID, label: ac.DisplayName()}
		if inst, ok := registry.GetConfigured(hc.id); ok && inst.HumanModel != "" {
			hc.agent = inst
		}
		return hc, true
	}
	return humanChat{}, false
}

// fromPerson reports whether msg was written by someone, as opposed to
// published by claw itself (bus.InboundMessage.Internal).
func fromPerson(msg bus.InboundMessage) bool {
	return !msg.Internal
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
	if reply, ok := al.humans.recentlyExpired(hc.id, window); ok {
		al.replyInHumanChat(ctx, msg, reply)
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
