// ClawEh
// License: MIT

package agent

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/channels"
	"github.com/PivotLLM/ClawEh/commands"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/constants"
	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/routing"
	"github.com/PivotLLM/ClawEh/tools"
	toolsagents "github.com/PivotLLM/ClawEh/tools/agents"
	"github.com/PivotLLM/ClawEh/utils"
)

// sessionIdleTTL is how long a session's dispatch state survives with no turn
// running or queued before pruneSessions drops it.
const sessionIdleTTL = time.Hour

// errCancelledByUser is the cause /cancel gives the running turn's context, so
// the turn's failure renders as a cancellation rather than a timeout.
var errCancelledByUser = errors.New("cancelled by /cancel")

// agentGoneText is the reply to a sender that required one from an agent that
// no longer exists.
func agentGoneText(agentID string) string {
	if agentID == "" {
		return "That agent no longer exists."
	}
	return "Agent " + agentID + " no longer exists."
}

// errAgentGone marks a message addressed to an agent that does not exist (a
// deleted temporary agent): it is dropped, never given to another agent.
var errAgentGone = errors.New("addressed agent does not exist")

// sessionState is one session's dispatch state. The first message to arrive
// while the session is idle makes its goroutine the owner: it runs turns until
// pending is empty, so at most one goroutine per session is ever running or
// waiting. Messages arriving while the owner is busy are appended to pending
// and their goroutines return at once; the owner then runs each chat's queued
// messages as one merged turn (see takeBatch). refs and lastUsed are guarded by
// AgentLoop.sessionsMu; everything else by mu.
type sessionState struct {
	refs     int       // goroutines holding the entry; pruned only at zero
	lastUsed time.Time // set on release

	mu         sync.Mutex
	busy       bool                    // an owner goroutine is draining pending
	pending    []bus.InboundMessage    // arrived while busy, in arrival order
	turnCancel context.CancelCauseFunc // cancels the running turn; nil when idle
	// cancelledRunning and skipCount record what /cancel did, for its reply.
	cancelledRunning bool
	skipCount        int
}

// acquireSession returns the dispatch state for key, creating it if needed, and
// takes a reference that keeps pruneIdleSessions from dropping it.
func (al *AgentLoop) acquireSession(key string) *sessionState {
	al.sessionsMu.Lock()
	defer al.sessionsMu.Unlock()
	if al.sessions == nil {
		al.sessions = make(map[string]*sessionState)
	}
	ss := al.sessions[key]
	if ss == nil {
		ss = &sessionState{}
		al.sessions[key] = ss
	}
	ss.refs++
	return ss
}

// releaseSession drops the reference taken by acquireSession.
func (al *AgentLoop) releaseSession(ss *sessionState) {
	al.sessionsMu.Lock()
	ss.refs--
	ss.lastUsed = time.Now()
	al.sessionsMu.Unlock()
}

// pruneIdleSessions drops every session entry with no holder that has been idle
// longer than ttl, returning how many it removed. A held entry (a turn running
// or queued, or a /cancel in flight) is never removed.
func (al *AgentLoop) pruneIdleSessions(now time.Time, ttl time.Duration) int {
	al.sessionsMu.Lock()
	defer al.sessionsMu.Unlock()
	removed := 0
	for key, ss := range al.sessions {
		if ss.refs == 0 && now.Sub(ss.lastUsed) > ttl {
			delete(al.sessions, key)
			removed++
		}
	}
	return removed
}

// pruneSessions runs until evictStop is closed, dropping idle session state
// every evictInterval.
func (al *AgentLoop) pruneSessions() {
	ticker := time.NewTicker(al.evictInterval)
	defer ticker.Stop()
	for {
		select {
		case <-al.evictStop:
			return
		case <-ticker.C:
			al.pruneIdleSessions(time.Now(), sessionIdleTTL)
		}
	}
}

// cancel stops the session's running turn, if any, and drops everything queued
// behind it, recording both for the /cancel reply. It returns the dropped
// messages.
func (ss *sessionState) cancel() []bus.InboundMessage {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	dropped := ss.pending
	ss.skipCount += len(ss.pending)
	ss.pending = nil
	if ss.turnCancel != nil {
		ss.turnCancel(errCancelledByUser)
		ss.turnCancel = nil
		ss.cancelledRunning = true
	}
	return dropped
}

// takeCancelResult reads and resets what /cancel recorded for the session:
// whether it stopped a running turn and how many queued messages it dropped.
// Nothing is recorded for an unknown session.
func (al *AgentLoop) takeCancelResult(key string) (running bool, skipped int) {
	al.sessionsMu.Lock()
	ss := al.sessions[key]
	al.sessionsMu.Unlock()
	if ss == nil {
		return false, 0
	}
	ss.mu.Lock()
	defer ss.mu.Unlock()
	running, skipped = ss.cancelledRunning, ss.skipCount
	ss.cancelledRunning, ss.skipCount = false, 0
	return running, skipped
}

// takeBatch removes the next turn's messages from pending and returns them
// merged: the oldest message plus every later message from the same
// channel:chat, up to the next command in that chat. Messages from other chats
// stay queued (an agent's session may serve several chats, whose messages are
// never merged), and a command always runs as a turn of its own, as does a
// message that requires its own reply (bus.MetaReplyRequired). With
// eachAlone (the session's agent is single-shot) every message is a turn of
// its own.
func takeBatch(pending *[]bus.InboundMessage, eachAlone bool) (bus.InboundMessage, bool) {
	queue := *pending
	if len(queue) == 0 {
		return bus.InboundMessage{}, false
	}
	first := queue[0]
	chat := first.Channel + ":" + first.ChatID
	batch := []bus.InboundMessage{first}
	rest := make([]bus.InboundMessage, 0, len(queue)-1)
	stop := eachAlone || runsAlone(first)
	for _, m := range queue[1:] {
		if !stop && m.Channel+":"+m.ChatID == chat {
			if !runsAlone(m) {
				batch = append(batch, m)
				continue
			}
			stop = true
		}
		rest = append(rest, m)
	}
	*pending = rest
	return mergeMessages(batch), true
}

// runsAlone reports whether a message must be a turn of its own, never merged
// with others: a command, or a message that requires its own final reply.
func runsAlone(m bus.InboundMessage) bool {
	return commands.HasCommandPrefix(m.Content) || m.ReplyRequired()
}

// mergeMessages joins a batch into one message: the contents newline-separated
// in arrival order, every attachment kept, and the reply addressed to the last
// message.
func mergeMessages(batch []bus.InboundMessage) bus.InboundMessage {
	if len(batch) == 1 {
		return batch[0]
	}
	merged := batch[len(batch)-1]
	parts := make([]string, 0, len(batch))
	var media []string
	for _, m := range batch {
		parts = append(parts, m.Content)
		media = append(media, m.Media...)
	}
	merged.Content = strings.Join(parts, "\n")
	merged.Media = media
	// Someone's message merged with claw's own is someone's.
	merged.Internal = !slices.ContainsFunc(batch, func(m bus.InboundMessage) bool { return !m.Internal })
	return merged
}

// acquireTurnSlot waits for a free concurrent-turn slot, or returns false when
// ctx ends first. Always true when no limit is configured.
func (al *AgentLoop) acquireTurnSlot(ctx context.Context) bool {
	if al.turnSem == nil {
		return true
	}
	select {
	case al.turnSem <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (al *AgentLoop) releaseTurnSlot() {
	if al.turnSem != nil {
		<-al.turnSem
	}
}

// isCancelCommand returns true if the message content is a /cancel command.
func (al *AgentLoop) isCancelCommand(content string) bool {
	name, ok := commands.ParseCommandName(content)
	return ok && name == "cancel"
}

// processSessionMessage dispatches one inbound message within its session.
// Messages sharing a session scope key (resolved via resolveMessageRoute) never
// process concurrently — including channel:chatID pairs that map to the same
// agent session — while different sessions never block each other. When the
// session is busy the message is queued for the owner goroutine and this one
// returns; a /cancel skips the queue entirely, stopping the running turn and
// dropping what is queued behind it before replying.
func (al *AgentLoop) processSessionMessage(ctx context.Context, msg bus.InboundMessage) {
	defer al.activeRequests.Done()

	// A person writing in their human agent's chat answers the request
	// waiting for them, or is told nothing is; neither is a turn. Decided
	// before the session is taken: the turn waiting for the answer holds it.
	if al.handleHumanChat(ctx, msg) {
		return
	}

	// Strip agent mention trigger if present and record the target agent in metadata
	// before resolving the route, so that mention routing is reflected in dispatchKey
	// and session key scoping.
	al.extractMention(&msg)

	// Resolve the route before dispatch so that all channel:chatID pairs that
	// share the same agent session use the same dispatch key. This prevents
	// concurrent LLM history reads/writes on the agent's one session.
	var dispatchKey string
	route, routed, routeErr := al.resolveMessageRoute(msg)
	// A single-shot agent answers every message on a blank context, so its
	// queued messages are never merged into one turn. The session's messages
	// all go to the one agent its scope key names.
	singleShot := routeErr == nil && routed != nil && routed.Spec.SingleShot()
	if routeErr != nil {
		// Fall back to channel:chatID if routing fails; processMessage will
		// return the same error and report it to the user.
		dispatchKey = msg.Channel + ":" + msg.ChatID
	} else {
		scopeKey := resolveScopeKey(route, msg.SessionKey)
		dispatchKey = scopeKey
		// Pre-stamp the session key so processMessage's internal call to
		// resolveScopeKey returns the same value without re-doing routing.
		msg.SessionKey = scopeKey
	}

	ss := al.acquireSession(dispatchKey)
	defer al.releaseSession(ss)

	if al.isCancelCommand(msg.Content) {
		dropped := ss.cancel()
		al.publishCancelledReplies(ctx, dropped)
		al.runTurn(ctx, ctx, msg)
		return
	}

	ss.mu.Lock()
	ss.pending = append(ss.pending, msg)
	if ss.busy {
		ss.mu.Unlock()
		return
	}
	ss.busy = true
	ss.mu.Unlock()

	for {
		ss.mu.Lock()
		batch, ok := takeBatch(&ss.pending, singleShot)
		if !ok || ctx.Err() != nil {
			ss.busy = false
			ss.mu.Unlock()
			return
		}
		// Registered in the same critical section as the take, so a /cancel
		// always sees the message either queued or running.
		turnCtx, cancelTurn := context.WithCancelCause(ctx)
		ss.turnCancel = cancelTurn
		ss.mu.Unlock()

		// An asked turn takes no slot: its asker lent its own while it waits,
		// so with every slot held by askers it could never run. Asks are
		// bounded by max_subagent_depth instead. Any other turn's slot
		// travels with it, so a wait inside it (an Ask, a request to a
		// person) can lend it out (turnSlot).
		slot := &turnSlot{al: al}
		if batch.Channel == constants.AgentMessageChannel {
			al.runTurn(ctx, turnCtx, batch)
		} else if slot.acquire(turnCtx) {
			func() {
				defer slot.release()
				al.runTurn(ctx, withTurnSlot(turnCtx, slot), batch)
			}()
		} else if errors.Is(context.Cause(turnCtx), errCancelledByUser) {
			// Cancelled while waiting for a turn slot: it never ran.
			al.publishCancelledReplies(ctx, []bus.InboundMessage{batch})
		}

		ss.mu.Lock()
		ss.turnCancel = nil
		ss.mu.Unlock()
		cancelTurn(nil)
	}
}

// runTurn processes one (possibly merged) message under the turn budget and
// publishes the reply. The turn runs under turnParent (which /cancel may end);
// the reply is published under ctx, the loop's run context, so a cancelled turn
// can still say so.
func (al *AgentLoop) runTurn(ctx, turnParent context.Context, msg bus.InboundMessage) {
	// An ask nobody waits for any more (timed out, or its asker stopped) is
	// not answered: no model turn runs for it.
	if msg.Channel == constants.AgentMessageChannel && !al.asks.isWaiting(msg.ChatID) {
		logger.InfoCF("agent", "Ask skipped: the asker stopped waiting",
			map[string]any{"ask_id": msg.ChatID, "agent_id": inboundMetadata(msg, metadataKeyPreresolvedAgentID)})
		return
	}
	var roundSent atomic.Bool
	// Overall turn budget: a hard backstop so a hung provider or tool can never
	// leave the user waiting forever. When it elapses the context is cancelled,
	// the LLM/tool loop unwinds, and we deliver a clear message below (which also
	// clears the typing indicator via the channel manager's preSend).
	turnTimeout := al.GetConfig().Agents.Defaults.GetTurnTimeout()
	// A request to a person may wait longer than a model turn would.
	turnTimeout = al.humanTurnBudget(msg, turnTimeout)
	turnCtx, turnCancel := context.WithTimeout(turnParent, turnTimeout)
	defer turnCancel()
	// An ask whose asker gives up on it (the forum's) ends the turn then,
	// so its model call is aborted rather than finishing for no one. A
	// person's turn withdraws its request instead (askHuman).
	if msg.Channel == constants.AgentMessageChannel && al.humanTarget(msg) == nil {
		if w, ok := al.asks.wait(msg.ChatID); ok && w.stopTurn {
			var stopTurn context.CancelCauseFunc
			turnCtx, stopTurn = context.WithCancelCause(turnCtx)
			defer stopTurn(nil)
			go func(ctx context.Context) {
				select {
				case <-w.gone:
					stopTurn(errAskerStopped)
				case <-ctx.Done():
				}
			}(turnCtx)
		}
	}
	// One id per turn, carried on the context so every log line and audit row
	// the turn produces can be pulled together.
	turnCtx = withTurnID(turnCtx, newTurnID())
	msgCtx := tools.WithRoundSentFlag(turnCtx, &roundSent)

	// outcome is set by the turn only when it did not end in a plain reply
	// (an empty one); failure and cancellation are decided here from err.
	var outcome string
	response, err := al.processMessageSafely(msgCtx, msg, &outcome)
	replyRequired := msg.ReplyRequired()
	if err != nil && shuttingDown(turnCtx) && al.humanTarget(msg) != nil {
		// A request to a person is never replayed (the person may already
		// have read it): it ends as cancelled, and a sender that requires a
		// reply gets one.
		logger.InfoCF("agent", "Request to a person cancelled by shutdown",
			turnFields(turnCtx, map[string]any{"channel": msg.Channel, "chat_id": msg.ChatID}))
		const shutdownText = "The request was cancelled because the service is shutting down."
		if msg.Channel == constants.AgentMessageChannel {
			al.deliverAskReply(msg, shutdownText, bus.OutcomeCancelled)
			return
		}
		if replyRequired {
			pubCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if perr := al.bus.PublishOutbound(pubCtx, bus.OutboundMessage{
				Channel: msg.Channel, ChatID: msg.ChatID, OriginalMessageID: msg.MessageID,
				Content: shutdownText, Outcome: bus.OutcomeCancelled,
			}); perr != nil {
				logger.WarnCF("agent", "Failed to publish cancelled reply", map[string]any{"channel": msg.Channel, "error": perr.Error()})
			}
		}
		return
	}
	if err != nil && shuttingDown(turnCtx) {
		// Interrupted, not failed: the turn stays pending and is replayed on
		// restart, so nothing is sent now.
		logger.InfoCF("agent", "Turn interrupted by shutdown; it is replayed on restart",
			turnFields(turnCtx, map[string]any{"channel": msg.Channel, "chat_id": msg.ChatID}))
		return
	}
	switch {
	case errors.Is(err, errAgentGone):
		// Addressed to an agent that no longer exists (a deleted temporary
		// agent): dropped, logged where it was detected, and never handed to
		// another agent. A sender that requires a reply is told it failed.
		if !replyRequired {
			return
		}
		response, outcome = agentGoneText(inboundMetadata(msg, metadataKeyPreresolvedAgentID)), bus.OutcomeError
	case errors.As(err, new(humanNotAskedError)):
		// Dropped (logged where detected): a person takes only questions from
		// agents. Someone who wrote to it (a mention, a chat, a device) or a
		// sender that requires a reply is told so, which also clears the
		// chat's indicators; claw's own messages are dropped silently.
		if !replyRequired && !fromPerson(msg) {
			return
		}
		response, outcome = err.Error(), bus.OutcomeError
	case err != nil && msg.Channel == constants.AgentMessageChannel && errors.Is(context.Cause(turnCtx), errAskerStopped):
		// The asker stopped waiting and the turn was cancelled for it
		// (askStoppingTurn): nobody is waiting for a reply.
		logger.InfoCF("agent", "Asked turn cancelled: the asker stopped waiting",
			turnFields(turnCtx, map[string]any{"ask_id": msg.ChatID, "agent_id": inboundMetadata(msg, metadataKeyPreresolvedAgentID)}))
		return
	case errors.Is(err, errAskerStopped):
		// The asker stopped waiting for a person's answer: it has its own
		// outcome, and the person was told (askHuman).
		logger.InfoCF("agent", "Request to a person ended: the asker stopped waiting",
			turnFields(turnCtx, map[string]any{"ask_id": msg.ChatID}))
		return
	case errors.Is(err, errHumanCancelled):
		response, outcome = err.Error(), bus.OutcomeCancelled
	case errors.As(err, new(humanUnreachableError)):
		// The request never reached the person's chat (logged in askHuman).
		response, outcome = err.Error(), bus.OutcomeError
	case err != nil && errors.Is(context.Cause(turnCtx), errCancelledByUser):
		response = "⚠️ Cancelled by /cancel. Some steps may have completed — ask me to continue if needed."
		outcome = bus.OutcomeCancelled
	case err != nil:
		assistant := ""
		if route, _, routeErr := al.resolveMessageRoute(msg); routeErr == nil {
			if a := al.GetConfig().AgentByID(route.AgentID); a != nil {
				assistant = a.DisplayName()
			}
		}
		response, outcome = renderTurnErrorFor(assistant, turnCtx, turnTimeout, err), bus.OutcomeError
	case response == "" || outcome == bus.OutcomeEmpty:
		outcome = bus.OutcomeEmpty
		if replyRequired {
			// The fallback advice is for a person; the sender gets the bare outcome.
			response = ""
		}
	case outcome == "":
		outcome = bus.OutcomeOK
	}

	// An ask's reply goes back to the asker, never to a channel.
	if msg.Channel == constants.AgentMessageChannel {
		askOutcome := outcome
		switch {
		case errors.Is(err, errHumanCancelled):
			askOutcome = tools.OutcomePersonCancelled
		case errors.As(err, new(humanUnreachableError)):
			askOutcome = tools.OutcomePersonUnreachable
		}
		if !al.deliverAskReply(msg, response, askOutcome) && err == nil && outcome == bus.OutcomeOK {
			// A person's answer that arrived as the asker stopped waiting:
			// they are told it is no longer needed rather than left unsure.
			al.humanAnswerUnused(ctx, msg)
		}
		return
	}

	// A required reply is always sent, even after msg_send replied in the turn.
	if replyRequired || (response != "" && !roundSent.Load()) {
		if err := al.bus.PublishOutbound(ctx, bus.OutboundMessage{
			Channel:           msg.Channel,
			ChatID:            msg.ChatID,
			Content:           response,
			OriginalMessageID: msg.MessageID,
			Outcome:           outcome,
		}); err != nil {
			logger.WarnCF("agent", "Failed to publish outbound response",
				turnFields(turnCtx, map[string]any{
					"channel": msg.Channel,
					"chat_id": msg.ChatID,
					"error":   err.Error(),
				}))
		} else {
			logger.InfoCF("agent", "Published outbound response",
				turnFields(turnCtx, map[string]any{
					"channel":     msg.Channel,
					"chat_id":     msg.ChatID,
					"content_len": len(response),
					"outcome":     outcome,
				}))
		}
	} else if roundSent.Load() && response != "" {
		logger.DebugCF("agent", "Skipped outbound (message tool already sent)",
			turnFields(turnCtx, map[string]any{"channel": msg.Channel}))
	}
}

// publishCancelledReplies sends the final "cancelled" reply for each message
// that requires one (bus.MetaReplyRequired) and was dropped by /cancel before
// its turn ran. Messages without the flag get nothing.
func (al *AgentLoop) publishCancelledReplies(ctx context.Context, msgs []bus.InboundMessage) {
	for _, m := range msgs {
		if !m.ReplyRequired() {
			continue
		}
		if m.Channel == constants.AgentMessageChannel {
			al.deliverAskReply(m, "Cancelled by /cancel before it started.", bus.OutcomeCancelled)
			continue
		}
		if err := al.bus.PublishOutbound(ctx, bus.OutboundMessage{
			Channel:           m.Channel,
			ChatID:            m.ChatID,
			Content:           "⚠️ Cancelled by /cancel before it started.",
			OriginalMessageID: m.MessageID,
			Outcome:           bus.OutcomeCancelled,
		}); err != nil {
			logger.WarnCF("agent", "Failed to publish cancelled reply",
				map[string]any{"channel": m.Channel, "chat_id": m.ChatID, "error": err.Error()})
		}
	}
}

// HandleExternalMessage delivers a raw external-message body to an agent as an
// unsolicited event — the same way a cron job fires (see schedule.ExecuteJob). It
// does NOT continue any existing conversation: it resolves the agent's default
// channel via CronTarget and publishes an inbound message there, so a monitoring
// webhook / GPS tracker / alarm reaches the agent even with no active chat.
//
// Returns ErrNoDefaultChannel (wrapped) when the agent has no default channel
// binding, so the HTTP layer can report a precondition (4xx) rather than a 500.
//
// The body is wrapped with the security prefix (so the model treats external
// input with caution) but the raw text is preserved intact. Delivery mirrors cron
// exactly: same CronTarget resolution and a fixed SenderID so downstream routing
// and dedupe treat it as a system-originated event.
func (al *AgentLoop) HandleExternalMessage(ctx context.Context, agentID, body string) error {
	cfg := al.GetConfig()
	if cfg == nil {
		return errors.New("configuration not loaded")
	}
	if cfg.IsHumanAgent(agentID) {
		return fmt.Errorf("%w: agent %q is a person and takes no external messages", ErrHumanAgent, agentID)
	}
	channel, chatID, peerKind, ok := cfg.CronTarget(agentID)
	if !ok {
		return fmt.Errorf("%w: agent %q — configure a default channel binding for it", ErrNoDefaultChannel, agentID)
	}

	prefix := global.DefaultMessagePrefix
	if cfg.Security.MessagePrefix != "" {
		prefix = cfg.Security.MessagePrefix
	}

	msg := bus.InboundMessage{
		Channel:  channel,
		SenderID: "webhook",
		ChatID:   chatID,
		Content:  prefix + body,
		Peer:     bus.Peer{Kind: peerKind, ID: chatID},
		Internal: true,
	}

	// Publish on a fresh bounded context (not the request context) so a client
	// that hangs up right after POSTing does not abort delivery — matching cron.
	pubCtx, pubCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer pubCancel()
	return al.bus.PublishInbound(pubCtx, msg)
}

func (al *AgentLoop) ProcessDirect(
	ctx context.Context,
	content, sessionKey string,
) (string, error) {
	return al.ProcessDirectWithChannel(ctx, content, sessionKey, "cli", "direct", "direct")
}

func (al *AgentLoop) ProcessDirectWithChannel(
	ctx context.Context,
	content, sessionKey, channel, chatID, peerKind string,
) (string, error) {
	if err := al.ensureMCPInitialized(ctx); err != nil {
		return "", err
	}

	msg := bus.InboundMessage{
		Channel:    channel,
		SenderID:   "cron",
		ChatID:     chatID,
		Content:    content,
		SessionKey: sessionKey,
		Internal:   true,
	}
	// Set peer so channel-based bindings (e.g. a specific Slack channel mapped
	// to a named agent) are matched by the route resolver, exactly as they are
	// for live inbound messages.
	if chatID != "" && chatID != "direct" {
		kind := peerKind
		if kind == "" {
			kind = "channel"
		}
		msg.Peer = bus.Peer{Kind: kind, ID: chatID}
	}

	return al.processMessage(ctx, msg)
}

// processMessageSafely runs processMessage with a panic guard so a bug in the
// turn (or a tool) is converted into a returned error and delivered to the user
// instead of crashing the per-message goroutine and leaving the typing indicator
// stuck. processSessionMessage runs us in our own goroutine, so an unrecovered
// panic here would otherwise take down the process.
//
// outcome, when non-nil, receives how the turn ended when that was not a plain
// reply (see processOptions.OutcomeOut).
func (al *AgentLoop) processMessageSafely(ctx context.Context, msg bus.InboundMessage, outcome *string) (resp string, err error) {
	defer func() {
		if r := recover(); r != nil {
			logger.ErrorCF("agent", "panic while processing message",
				map[string]any{
					"channel": msg.Channel,
					"chat_id": msg.ChatID,
					"panic":   fmt.Sprintf("%v", r),
					"stack":   string(debug.Stack()),
				})
			resp = ""
			err = fmt.Errorf("internal error while processing the message: %v", r)
		}
	}()
	return al.processMessageOutcome(ctx, msg, outcome)
}

func (al *AgentLoop) processMessage(ctx context.Context, msg bus.InboundMessage) (string, error) {
	return al.processMessageOutcome(ctx, msg, nil)
}

// processMessageOutcome is processMessage reporting through outcome (may be
// nil) how the turn ended when that was not a plain reply.
func (al *AgentLoop) processMessageOutcome(ctx context.Context, msg bus.InboundMessage, outcome *string) (string, error) {
	// Direct callers (CLI, cron, external messages) do not come through runTurn;
	// give them a turn id too so their logs and audit rows are correlated.
	if turnIDFrom(ctx) == "" {
		ctx = withTurnID(ctx, newTurnID())
	}
	logFields := turnFields(ctx, map[string]any{
		"channel":     msg.Channel,
		"chat_id":     msg.ChatID,
		"sender_id":   msg.SenderID,
		"session_key": msg.SessionKey,
	})
	if logger.GetLogMessageContent() {
		var logContent string
		if strings.Contains(msg.Content, "Error:") || strings.Contains(msg.Content, "error") {
			logContent = msg.Content // Full content for errors
		} else {
			logContent = utils.Truncate(msg.Content, 80)
		}
		logFields["preview"] = logContent
	}
	logger.InfoCF(
		"agent",
		fmt.Sprintf("Processing message from %s:%s", msg.Channel, msg.SenderID),
		logFields,
	)

	var hadAudio bool
	// A request to a person is relayed as written: no transcription model
	// sees a human agent's conversation.
	if al.humanTarget(msg) == nil {
		msg, hadAudio = al.transcribeAudioInMessage(ctx, msg)
	}

	// For audio messages the placeholder was deferred by the channel.
	// Now that transcription (and optional feedback) is done, send it.
	if hadAudio && al.channelManager != nil {
		al.channelManager.SendPlaceholder(ctx, msg.Channel, msg.ChatID)
	}

	maxDepth := config.DefaultMaxSubagentDepth
	if cfg := al.GetConfig(); cfg != nil {
		maxDepth = cfg.Agents.Defaults.GetMaxSubagentDepth()
	}
	ctx = withInboundSpawnDepth(ctx, msg, maxDepth)
	ctx = withInboundAskChain(ctx, msg)

	// Route system messages to processSystemMessage
	if msg.Channel == "system" {
		return al.processSystemMessage(ctx, msg, outcome)
	}

	// Extract agent mention from trigger prefix (e.g. "@alice do X" → routes to alice with "do X")
	al.extractMention(&msg)

	route, agent, routeErr := al.resolveMessageRoute(msg)
	if routeErr != nil {
		return "", routeErr
	}

	// Detect whether a mention caused routing to a specific agent so we can
	// attribute the response. A mention is "honored" when the extracted agent
	// name matches the resolved agent ID (i.e. routing was overridden by the mention).
	mentionedAgent := inboundMetadata(msg, "mentioned_agent")
	mentionHonored := mentionedAgent != "" && strings.EqualFold(route.AgentID, mentionedAgent)

	// Legacy: reset the shared sentInRound flag on the message tool. The concurrent
	// dispatch path uses per-round context flags instead, so this is a no-op there.
	messageTool, hasMsg := agent.Tools.Get("message")
	if !hasMsg {
		messageTool, hasMsg = agent.Tools.Get("msg_send")
	}
	if hasMsg {
		tool := messageTool
		if resetter, ok := tool.(interface{ ResetSentInRound() }); ok {
			resetter.ResetSentInRound()
		}
	}

	// Resolve session key from route, while preserving explicit agent-scoped keys.
	scopeKey := resolveScopeKey(route, msg.SessionKey)
	sessionKey := scopeKey

	logger.InfoCF("agent", "Routed message",
		turnFields(ctx, map[string]any{
			"agent_id":      agent.ID,
			"scope_key":     scopeKey,
			"session_key":   sessionKey,
			"matched_by":    route.MatchedBy,
			"route_agent":   route.AgentID,
			"route_channel": route.Channel,
		}))

	userContent := prependSenderLabel(msg.Content, msg.Sender)
	// Drop received attachments into the agent's workspace so its file tools can read
	// them (the model otherwise only sees an annotation it can't open).
	// Not for a fresh temporary agent: it has no file tools to read them
	// with, and its workspace stays empty. The media refs still reach the
	// model with the message.
	if !agent.Spec.Fresh {
		userContent += al.materializeInboundMedia(msg, agent)
	}

	opts := processOptions{
		SessionKey:      sessionKey,
		Channel:         msg.Channel,
		ChatID:          msg.ChatID,
		UserMessage:     userContent,
		Media:           msg.Media,
		DefaultResponse: defaultResponse,
		SendResponse:    false,
		IsRetry:         msg.IsRetry,
		ResetSession:    msg.Metadata[metaSessionReset] == "true",
		SenderID:        msg.SenderID,
		SenderName:      senderSource(msg.SenderID, msg.Sender),
		IsGroup:         inboundMetadata(msg, "is_group") == "true",
		OutcomeOut:      outcome,
		MessageID:       msg.MessageID,
		ReplyRequired:   msg.ReplyRequired(),
	}

	// context-dependent commands check their own Runtime fields and report
	// "unavailable" when the required capability is nil.
	if response, handled := al.handleCommand(ctx, msg, agent, &opts); handled {
		if agent.Spec.SingleShot() {
			// Whatever the command opened or wrote (a /compact, a session
			// reset) is not kept by an agent that keeps nothing.
			al.discardConversation(context.WithoutCancel(ctx), agent, sessionKey)
		}
		return response, nil
	}

	response, err := al.runMeteredTurn(ctx, agent, opts)
	if err != nil {
		return response, err
	}

	if mentionHonored && response != "" {
		displayName := agent.Name
		if displayName == "" {
			displayName = agent.ID
		}
		response = "**" + displayName + ":**\n" + response
	}

	return response, nil
}

func (al *AgentLoop) resolveMessageRoute(msg bus.InboundMessage) (routing.ResolvedRoute, *AgentInstance, error) {
	registry := al.GetRegistry()

	// Honor an explicit preresolved agent ID (set by trusted internal callers
	// such as the callback HTTP handler). This bypasses binding-based routing
	// so the message is delivered to the named agent regardless of which
	// bindings would otherwise match the channel/peer/account cascade.
	//
	// A message addressed to an agent that does not exist (a temporary agent
	// deleted since, or an agent removed from the config) is dropped: handing
	// it to binding routing or the default agent would deliver one agent's
	// traffic — a session_clear, say — to another.
	if preresolved := inboundMetadata(msg, metadataKeyPreresolvedAgentID); preresolved != "" {
		normalized := routing.NormalizeAgentID(preresolved)
		agent, ok := registry.Get(normalized)
		if !ok {
			logger.WarnCF("agent", "Message dropped: addressed agent does not exist",
				map[string]any{"preresolved_agent_id": preresolved, "channel": msg.Channel, "sender_id": msg.SenderID})
			return routing.ResolvedRoute{}, nil, fmt.Errorf("%w: %s", errAgentGone, preresolved)
		}
		route := routing.ResolvedRoute{
			AgentID:    normalized,
			Channel:    msg.Channel,
			AccountID:  routing.NormalizeAccountID(inboundMetadata(msg, metadataKeyAccountID)),
			SessionKey: routing.BuildAgentMainSessionKey(normalized),
			MatchedBy:  "preresolved",
		}
		return route, agent, nil
	}

	route := registry.ResolveRoute(routing.RouteInput{
		Channel:        msg.Channel,
		AccountID:      inboundMetadata(msg, metadataKeyAccountID),
		Peer:           extractPeer(msg),
		ParentPeer:     extractParentPeer(msg),
		GuildID:        inboundMetadata(msg, metadataKeyGuildID),
		TeamID:         inboundMetadata(msg, metadataKeyTeamID),
		MentionedAgent: inboundMetadata(msg, "mentioned_agent"),
	})

	// Bindings and mentions only ever name config agents.
	agent, ok := registry.GetConfigured(route.AgentID)
	if !ok {
		agent = registry.Default()
	}
	if agent == nil {
		return routing.ResolvedRoute{}, nil, fmt.Errorf("no agent available for route (agent_id=%s)", route.AgentID)
	}

	return route, agent, nil
}

// resolveScopeKey returns the session an inbound message runs in: the routed
// agent's main conversation. Any explicit key collapses to it, so no inbound
// path can open a second session for an agent (its cognitive memory is fed
// from exactly one).
func resolveScopeKey(route routing.ResolvedRoute, msgSessionKey string) string {
	return routing.ResolveAgentSessionKey(route.AgentID, msgSessionKey)
}

// extractMention checks for and strips an agent mention trigger from msg.Content,
// recording the target agent in msg.Metadata["mentioned_agent"]. Idempotent: a
// message that already carries a mention is left alone, so the route chosen
// for the dispatch mutex in processSessionMessage is the route processMessage
// dispatches on, even when the stripped content starts with another mention.
func (al *AgentLoop) extractMention(msg *bus.InboundMessage) {
	if msg == nil || msg.Metadata["mentioned_agent"] != "" {
		return
	}
	cfg := al.GetConfig()
	var triggers []string
	if cfg != nil {
		triggers = cfg.AgentMentions.Triggers
	}
	if len(triggers) == 0 {
		triggers = []string{"@", "/", "."}
	}
	registry := al.GetRegistry()
	if registry == nil {
		return
	}
	agentIDs := registry.List()
	if mentionedAgent, stripped := channels.ExtractAgentMention(msg.Content, triggers, agentIDs); mentionedAgent != "" {
		msg.Content = stripped
		if msg.Metadata == nil {
			msg.Metadata = make(map[string]string)
		}
		msg.Metadata["mentioned_agent"] = mentionedAgent
	}
}

func (al *AgentLoop) processSystemMessage(
	ctx context.Context,
	msg bus.InboundMessage,
	outcome *string,
) (string, error) {
	if msg.Channel != "system" {
		return "", fmt.Errorf(
			"processSystemMessage called with non-system message channel: %s",
			msg.Channel,
		)
	}

	logger.InfoCF("agent", "Processing system message",
		map[string]any{
			"sender_id": msg.SenderID,
			"chat_id":   msg.ChatID,
		})

	// Parse origin channel from chat_id (format: "channel:chat_id")
	var originChannel, originChatID string
	if idx := strings.Index(msg.ChatID, ":"); idx > 0 {
		originChannel = msg.ChatID[:idx]
		originChatID = msg.ChatID[idx+1:]
	} else {
		originChannel = "cli"
		originChatID = msg.ChatID
	}

	// Extract subagent result from message content
	// Format: "Task 'label' completed.\n\nResult:\n<actual content>"
	content := msg.Content
	if idx := strings.Index(content, "Result:\n"); idx >= 0 {
		content = content[idx+8:] // Extract just the result part
	}

	// A background result of an asked turn: the asker has its reply already,
	// so the result goes to the agent's own main conversation only. The turn
	// runs on the ask channel (no chat id): nothing is sent to any chat, no
	// chat becomes the session's source, and no recovery record is kept.
	askOrigin := originChannel == constants.AgentMessageChannel
	if askOrigin {
		originChatID = ""
	}

	// Skip internal channels - only log, don't send to user
	if !askOrigin && constants.IsInternalChannel(originChannel) {
		logger.InfoCF("agent", "Subagent completed (internal channel)",
			map[string]any{
				"sender_id":   msg.SenderID,
				"content_len": len(content),
				"channel":     originChannel,
			})
		return "", nil
	}

	agent, sessionKey := al.resolveSystemMessageTarget(msg)
	if agent == nil {
		if inboundMetadata(msg, metadataKeyPreresolvedAgentID) != "" {
			return "", errAgentGone
		}
		return "", errors.New("no agent available for system message")
	}

	// An async sub-agent result is a tool result that arrives late: cap it as
	// the synchronous path does, or an oversized one fails the turn it lands in.
	result := capToolResult(msg.Content, false, agent.ContextWindow)
	if len(result) != len(msg.Content) {
		logger.WarnCF("agent", "Async task result truncated for context", map[string]any{
			"agent_id":     agent.ID,
			"sender_id":    msg.SenderID,
			"original_len": len(msg.Content),
			"kept_len":     len(result),
		})
	}

	response, err := al.runMeteredTurn(ctx, agent, processOptions{
		SessionKey:      sessionKey,
		Channel:         originChannel,
		ChatID:          originChatID,
		UserMessage:     fmt.Sprintf("[System: %s] %s", msg.SenderID, result),
		DefaultResponse: "Background task completed.",
		SendResponse:    !askOrigin,
		OutcomeOut:      outcome,
		OnReplyDelivery: al.systemReplyFallback(ctx, msg, agent.ID, originChannel, originChatID),
	})
	if askOrigin && err == nil {
		// Kept in the conversation; not sent anywhere.
		logger.InfoCF("agent", "Background result of an asked turn kept in the main conversation",
			map[string]any{"agent_id": agent.ID, "sender_id": msg.SenderID, "reply_len": len(response)})
		return "", nil
	}
	return response, err
}

// Metadata of a "system" message naming the chat its reply falls back to
// when the chat in its ChatID is offline or unknown (the forum's notice:
// the launching chat first, the launcher's default chat after).
const (
	metadataKeyFallbackChannel = "fallback_channel"
	metadataKeyFallbackChatID  = "fallback_chat_id"
)

// systemReplyFallback returns the OnReplyDelivery of a system message's
// reply: when msg names a fallback chat and the reply could not reach its
// own chat because the recipient is offline or not found, the same reply is
// sent to the fallback chat. It returns nil when msg names no fallback.
func (al *AgentLoop) systemReplyFallback(ctx context.Context, msg bus.InboundMessage, agentID, channel, chatID string) func(string, error) {
	fbChannel := inboundMetadata(msg, metadataKeyFallbackChannel)
	fbChatID := inboundMetadata(msg, metadataKeyFallbackChatID)
	if fbChannel == "" || fbChatID == "" {
		return nil
	}
	ctx = context.WithoutCancel(ctx)
	return func(reply string, err error) {
		if !errors.Is(err, channels.ErrRecipientOffline) && !errors.Is(err, channels.ErrRecipientNotFound) {
			return
		}
		fields := map[string]any{
			"agent_id": agentID, "sender_id": msg.SenderID, "channel": channel, "chat_id": chatID,
			"fallback_channel": fbChannel, "fallback_chat_id": fbChatID, "error": err.Error(),
		}
		// OnDelivery must not block: the fallback is published on its own.
		go func() {
			pubCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			if perr := al.bus.PublishOutbound(pubCtx, bus.OutboundMessage{Channel: fbChannel, ChatID: fbChatID, Content: reply}); perr != nil {
				fields["publish_error"] = perr.Error()
				logger.WarnCF("agent", "Reply's chat unavailable; the fallback chat could not be queued either", fields)
				return
			}
			logger.InfoCF("agent", "Reply's chat unavailable; reply sent to the fallback chat", fields)
		}()
	}
}

// runMeteredTurn runs the turn with usage accounting and folds its cost into
// the day's spend total (see recordSpend).
func (al *AgentLoop) runMeteredTurn(ctx context.Context, agent *AgentInstance, opts processOptions) (string, error) {
	var usage global.TurnUsage
	opts.UsageOut = &usage
	response, err := al.runAgentLoop(ctx, agent, opts)
	al.recordSpend(usage.CostUSD)
	return response, err
}

// resolveSystemMessageTarget picks the agent and session a "system" message
// (e.g. an async tool/sub-agent completion) should be processed in. A spawn or
// other async tool carries the originating agent on preresolved_agent_id and its
// session on session_key, so the completion is handled by the SPAWNING agent in
// its own session — not whichever agent happens to be the default. Falls back to
// the default agent and that agent's main session when no originator is given.
// A preresolved agent that does not exist is never replaced
// by another: the message is dropped. Returns (nil, "") when no agent is
// available or the addressed one is gone.
func (al *AgentLoop) resolveSystemMessageTarget(msg bus.InboundMessage) (*AgentInstance, string) {
	var agent *AgentInstance
	if preresolved := inboundMetadata(msg, metadataKeyPreresolvedAgentID); preresolved != "" {
		a, ok := al.GetRegistry().Get(routing.NormalizeAgentID(preresolved))
		if !ok || a == nil {
			// Never another agent: the addressed one is gone.
			logger.WarnCF("agent", "System message dropped: addressed agent does not exist",
				map[string]any{"preresolved_agent_id": preresolved, "sender_id": msg.SenderID})
			return nil, ""
		}
		agent = a
	} else {
		agent = al.GetRegistry().Default()
	}
	if agent == nil {
		return nil, ""
	}

	// Any key resolves to the agent's one conversation.
	return agent, routing.ResolveAgentSessionKey(agent.ID, msg.SessionKey)
}

// extractPeer extracts the routing peer from the inbound message's structured Peer field.
func extractPeer(msg bus.InboundMessage) *routing.RoutePeer {
	if msg.Peer.Kind == "" {
		return nil
	}
	peerID := msg.Peer.ID
	if peerID == "" {
		if msg.Peer.Kind == "direct" {
			peerID = msg.SenderID
		} else {
			peerID = msg.ChatID
		}
	}
	return &routing.RoutePeer{Kind: msg.Peer.Kind, ID: peerID}
}

// withInboundSpawnDepth applies the sender's sub-agent depth
// (bus.MetaSpawnDepth) to the turn's context. It only ever raises the depth:
// a value at or below the context's own is ignored, and an invalid one is
// ignored with a log line. A value above maxDepth (the configured
// max_subagent_depth) is clamped to it.
func withInboundSpawnDepth(ctx context.Context, msg bus.InboundMessage, maxDepth int) context.Context {
	raw := inboundMetadata(msg, bus.MetaSpawnDepth)
	if raw == "" {
		return ctx
	}
	depth, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || depth < 0 {
		logger.WarnCF("agent", "Ignoring invalid spawn depth on inbound message",
			map[string]any{"channel": msg.Channel, "chat_id": msg.ChatID, "spawn_depth": raw})
		return ctx
	}
	depth = min(depth, maxDepth)
	if depth <= toolsagents.SpawnDepth(ctx) {
		return ctx
	}
	return toolsagents.WithSpawnDepth(ctx, depth)
}

func inboundMetadata(msg bus.InboundMessage, key string) string {
	if msg.Metadata == nil {
		return ""
	}
	return msg.Metadata[key]
}

// senderLabel returns a short human-readable identifier for a sender, used to
// prefix group/channel messages so the LLM knows who sent each turn.
// Falls back gracefully: DisplayName → "@Username" → empty (caller decides).
func senderLabel(s bus.SenderInfo) string {
	if s.DisplayName != "" && s.Username != "" {
		return s.DisplayName + " (@" + s.Username + ")"
	}
	if s.DisplayName != "" {
		return s.DisplayName
	}
	if s.Username != "" {
		return "@" + s.Username
	}
	return ""
}

// prependSenderLabel attributes a message with its sender ("[From: <label>]").
// Applied to every message, direct chats included: direct and group messages
// share the agent's one session, so a "private"
// message is just one of several senders in the shared context — the label keeps
// attribution unambiguous. Cheap, and the clarity is worth the few tokens. No-op
// when the sender yields no label.
func prependSenderLabel(content string, s bus.SenderInfo) string {
	label := senderLabel(s)
	if label == "" {
		return content
	}
	return "[From: " + label + "]\n" + content
}

// senderSource returns a human-readable source string for message archiving.
// Combines the display label with the canonical ID when both are available.
func senderSource(canonicalID string, s bus.SenderInfo) string {
	label := senderLabel(s)
	if label == "" {
		return canonicalID
	}
	if canonicalID == "" {
		return label
	}
	return label + " [" + canonicalID + "]"
}

// extractParentPeer extracts the parent peer (reply-to) from inbound message metadata.
func extractParentPeer(msg bus.InboundMessage) *routing.RoutePeer {
	parentKind := inboundMetadata(msg, metadataKeyParentPeerKind)
	parentID := inboundMetadata(msg, metadataKeyParentPeerID)
	if parentKind == "" || parentID == "" {
		return nil
	}
	return &routing.RoutePeer{Kind: parentKind, ID: parentID}
}
