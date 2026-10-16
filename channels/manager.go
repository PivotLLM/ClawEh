// ClawEh
// License: MIT

package channels

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tenebris-tech/alerter"
	"golang.org/x/time/rate"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/constants"
	"github.com/PivotLLM/ClawEh/health"
	"github.com/PivotLLM/ClawEh/internal/clock"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/media"
)

const (
	defaultChannelQueueSize = 16
	defaultRateLimit        = 10 // default 10 msg/s
	maxRetries              = 3
	rateLimitDelay          = 1 * time.Second
	baseBackoff             = 500 * time.Millisecond
	maxBackoff              = 8 * time.Second

	janitorInterval = 10 * time.Second
	typingStopTTL   = 5 * time.Minute
	placeholderTTL  = 10 * time.Minute

	// backoffJitter spreads retry delays by ±20% so channels that failed
	// together do not retry in lockstep.
	backoffJitter = 0.2
)

// jitter returns d varied by up to ±backoffJitter.
func jitter(d time.Duration) time.Duration {
	f := 1 + backoffJitter*(2*rand.Float64()-1) //nolint:gosec // retry spread, not security-relevant
	return time.Duration(float64(d) * f)
}

// typingEntry wraps a typing stop function with a creation timestamp for TTL eviction.
type typingEntry struct {
	stop      func()
	createdAt time.Time
}

// reactionEntry wraps a reaction undo function with a creation timestamp for TTL eviction.
type reactionEntry struct {
	undo      func()
	createdAt time.Time
}

// placeholderEntry wraps a placeholder ID with a creation timestamp for TTL eviction.
type placeholderEntry struct {
	id        string
	createdAt time.Time
}

// channelRateConfig maps channel name to per-second rate limit.
var channelRateConfig = map[string]float64{
	"telegram": 20,
	"discord":  1,
	"slack":    1,
	"matrix":   2,
}

type channelWorker struct {
	ch         Channel
	queue      chan bus.OutboundMessage
	mediaQueue chan bus.OutboundMediaMessage
	done       chan struct{}
	mediaDone  chan struct{}
	limiter    *rate.Limiter
	// backlog and mediaBacklog are what was held for the channel while it
	// started; the worker sends them before anything queued.
	backlog      []bus.OutboundMessage
	mediaBacklog []bus.OutboundMediaMessage
	// closeMu guards closed: an enqueue holds it for reading and closeQueues
	// for writing, so nothing is ever sent on a closed queue.
	closeMu sync.RWMutex
	closed  bool
}

// errWorkerClosed is returned by enqueueOn for a worker whose channel is being
// stopped or rebuilt; the dispatcher looks the channel up again.
var errWorkerClosed = fmt.Errorf("%w: its worker is closed", ErrNotRunning)

// enqueueOn queues msg on q, one of w's queues, unless w is closed, the
// goroutine reading q has exited (exited, its done channel), or ctx ends.
func enqueueOn[M any](ctx context.Context, w *channelWorker, q chan M, exited <-chan struct{}, msg M) error {
	w.closeMu.RLock()
	defer w.closeMu.RUnlock()
	if w.closed {
		return errWorkerClosed
	}
	select {
	case q <- msg:
		return nil
	case <-exited:
		return errWorkerClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

// closeQueues closes both of w's queues, once; its goroutines then finish what
// is queued and exit.
func (w *channelWorker) closeQueues() {
	w.closeMu.Lock()
	defer w.closeMu.Unlock()
	if w.closed {
		return
	}
	w.closed = true
	close(w.queue)
	close(w.mediaQueue)
}

type Manager struct {
	channels map[string]Channel
	workers  map[string]*channelWorker
	// inputs is the fingerprint of what each channel was built from (see
	// channelSpec); a reload rebuilds a channel only when it changes.
	inputs map[string]string
	// cancels ends each started channel's workers and start retries.
	cancels map[string]context.CancelFunc
	// startCancels cancels the context each channel was started with (see
	// startContext).
	startCancels map[string]context.CancelFunc
	// startCtx is cancelled by StopAll before it waits for the starts in
	// progress, so a slow Start (an unreachable API) does not hold up a
	// shutdown; every Start's context ends with it. startMu guards it.
	startMu      sync.Mutex
	startCtx     context.Context
	cancelStarts context.CancelFunc
	// held collects outbound messages for channels being started, delivered
	// in order once the start has finished (see releaseHold).
	held map[string]*heldOutbound
	// dispatchCtx is the dispatchers' context, set by StartAll; every
	// channel's context derives from it.
	dispatchCtx context.Context
	// specs is the spec each channel was last built or kept from.
	specs      map[string]channelSpec
	bus        *bus.MessageBus
	mediaStore media.MediaStore
	// discoveryFailing names the SecMsg daemons whose account discovery is
	// failing, so the alert is raised once per streak.
	discoveryMu      sync.Mutex
	discoveryFailing map[string]bool
	dispatchTask     *asyncTask
	// onBuild, when set, is handed every channel the manager builds, before
	// it starts (see WithBuildHook).
	onBuild func(name string, ch Channel)
	// reloadMu serializes Reload.
	reloadMu      sync.Mutex
	mu            sync.RWMutex
	placeholders  sync.Map // "channel:chatID" → placeholderEntry
	typingStops   sync.Map // "channel:chatID" → typingEntry
	reactionUndos sync.Map // "channel:chatID:messageID" → reactionEntry
	// alerter, when set, hears about channels that give up (start or send).
	alerterMu sync.RWMutex
	alerter   alerter.Alerter
	// clock times retries and their backoff; nil means clock.Real (tests set
	// a fake).
	clock clock.Clock
}

// clk is the manager's clock.
func (m *Manager) clk() clock.Clock { return clock.Or(m.clock) }

// SetAlerter routes channel failures to an alerter. It is also handed to every
// channel already registered, since the alerter is set after NewManager has
// created them.
func (m *Manager) SetAlerter(a alerter.Alerter) {
	m.alerterMu.Lock()
	m.alerter = a
	m.alerterMu.Unlock()

	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, ch := range m.channels {
		if setter, ok := ch.(interface{ SetAlerter(a alerter.Alerter) }); ok {
			setter.SetAlerter(a)
		}
	}
}

func (m *Manager) alert(a alerter.Alert) {
	m.alerterMu.RLock()
	al := m.alerter
	m.alerterMu.RUnlock()
	if al != nil {
		al.Send(a)
	}
}

// stopAllTimeout bounds how long StopAll will wait for channels to stop.
// It is a var so tests can override it.
var stopAllTimeout = 15 * time.Second

type asyncTask struct {
	cancel context.CancelFunc
}

// RecordPlaceholder registers a placeholder message for later editing.
// Implements PlaceholderRecorder.
func (m *Manager) RecordPlaceholder(channel, chatID, placeholderID string) {
	key := channel + ":" + chatID
	m.placeholders.Store(key, placeholderEntry{id: placeholderID, createdAt: time.Now()})
}

// UpdatePlaceholder edits the active placeholder for channel/chatID in place to
// show progress on a long-running turn. Unlike preSend it does NOT consume the
// placeholder (the final reply still edits it) and does NOT stop typing. It is a
// no-op when there is no recorded placeholder or the channel cannot edit
// messages. Returns true when the placeholder was updated.
func (m *Manager) UpdatePlaceholder(ctx context.Context, channel, chatID, text string) bool {
	key := channel + ":" + chatID
	v, ok := m.placeholders.Load(key)
	if !ok {
		return false
	}
	entry, ok := v.(placeholderEntry)
	if !ok || entry.id == "" {
		return false
	}
	m.mu.RLock()
	ch, ok := m.channels[channel]
	m.mu.RUnlock()
	if !ok {
		return false
	}
	editor, ok := ch.(MessageEditor)
	if !ok {
		return false
	}
	if err := editor.EditMessage(ctx, chatID, entry.id, text); err != nil {
		return false
	}
	return true
}

// SendPlaceholder sends a "Thinking…" placeholder for the given channel/chatID
// and records it for later editing. Returns true if a placeholder was sent.
func (m *Manager) SendPlaceholder(ctx context.Context, channel, chatID string) bool {
	m.mu.RLock()
	ch, ok := m.channels[channel]
	m.mu.RUnlock()
	if !ok {
		return false
	}
	pc, ok := ch.(PlaceholderCapable)
	if !ok {
		return false
	}
	phID, err := pc.SendPlaceholder(ctx, chatID)
	if err != nil || phID == "" {
		return false
	}
	m.RecordPlaceholder(channel, chatID, phID)
	return true
}

// RecordTypingStop registers a typing stop function for later invocation.
// Implements PlaceholderRecorder.
func (m *Manager) RecordTypingStop(channel, chatID string, stop func()) {
	key := channel + ":" + chatID
	entry := typingEntry{stop: stop, createdAt: time.Now()}
	if previous, loaded := m.typingStops.Swap(key, entry); loaded {
		if oldEntry, ok := previous.(typingEntry); ok && oldEntry.stop != nil {
			oldEntry.stop()
		}
	}
}

// StopTyping ends the typing indicator for a channel/chatID without sending a
// message. Normally typing is stopped in preSend when an outbound reply is sent,
// but a turn that produces no reply (e.g. an empty model response) must still
// clear the indicator so the user is not left waiting. Idempotent and a no-op
// when no typing indicator is active.
func (m *Manager) StopTyping(channel, chatID string) {
	key := channel + ":" + chatID
	if v, loaded := m.typingStops.LoadAndDelete(key); loaded {
		if entry, ok := v.(typingEntry); ok && entry.stop != nil {
			entry.stop()
		}
	}
}

// DismissInbound clears what the channel showed while an inbound message was
// being handled (typing, the reaction on messageID, the placeholder) when that
// message gets no reply of its own. A placeholder is deleted where the channel
// can, otherwise edited to a check mark. No-op for anything not recorded.
func (m *Manager) DismissInbound(ctx context.Context, channel, chatID, messageID string) {
	key := channel + ":" + chatID
	m.StopTyping(channel, chatID)
	if v, loaded := m.reactionUndos.LoadAndDelete(key + ":" + messageID); loaded {
		if entry, ok := v.(reactionEntry); ok {
			entry.undo()
		}
	}
	v, loaded := m.placeholders.LoadAndDelete(key)
	if !loaded {
		return
	}
	entry, ok := v.(placeholderEntry)
	if !ok || entry.id == "" {
		return
	}
	m.mu.RLock()
	ch, ok := m.channels[channel]
	m.mu.RUnlock()
	if !ok {
		return
	}
	if d, ok := ch.(MessageDeleter); ok && d.DeleteMessage(ctx, chatID, entry.id) == nil {
		return
	}
	if e, ok := ch.(MessageEditor); ok {
		if err := e.EditMessage(ctx, chatID, entry.id, "✓"); err != nil {
			logger.DebugCF("channels", "Placeholder not cleared", map[string]any{"channel": channel, "chat_id": chatID, "error": err.Error()})
		}
	}
}

// SupportsStreaming reports whether the named channel's owner implements
// StreamCapable. The agent loop uses this to decide whether to install the
// per-delta streaming callback for a turn; false means the channel is streamed
// nothing and behaves exactly as before.
func (m *Manager) SupportsStreaming(channel string) bool {
	m.mu.RLock()
	ch, ok := m.channels[channel]
	m.mu.RUnlock()
	if !ok {
		return false
	}
	_, ok = ch.(StreamCapable)
	return ok
}

// StreamDelta forwards a coalesced partial-assistant-text delta to the named
// channel if its owner implements StreamCapable; a no-op otherwise. Errors are
// swallowed: streaming is best-effort progress and the terminal reply (Send)
// remains authoritative.
func (m *Manager) StreamDelta(ctx context.Context, channel, chatID, delta string) {
	m.mu.RLock()
	ch, ok := m.channels[channel]
	m.mu.RUnlock()
	if !ok {
		return
	}
	sc, ok := ch.(StreamCapable)
	if !ok {
		return
	}
	if err := sc.StreamDelta(ctx, chatID, delta); err != nil {
		logger.DebugCF("channels", "Stream delta failed", map[string]any{
			"channel": channel, "chat_id": chatID, "error": err.Error(),
		})
	}
}

// RecordReactionUndo registers a reaction undo function for later invocation.
// Implements PlaceholderRecorder.
func (m *Manager) RecordReactionUndo(channel, chatID, messageID string, undo func()) {
	key := channel + ":" + chatID + ":" + messageID
	m.reactionUndos.Store(key, reactionEntry{undo: undo, createdAt: time.Now()})
}

// preSend handles typing stop, reaction undo, and placeholder editing before sending a message.
// Returns true if the message was edited into a placeholder (skip Send).
func (m *Manager) preSend(ctx context.Context, name string, msg bus.OutboundMessage, ch Channel) bool {
	key := name + ":" + msg.ChatID

	// 1. Stop typing
	if v, loaded := m.typingStops.LoadAndDelete(key); loaded {
		if entry, ok := v.(typingEntry); ok {
			entry.stop() // idempotent, safe
		}
	}

	// 2. Undo reaction
	undoKey := name + ":" + msg.ChatID + ":" + msg.OriginalMessageID
	if v, loaded := m.reactionUndos.LoadAndDelete(undoKey); loaded {
		if entry, ok := v.(reactionEntry); ok {
			entry.undo() // idempotent, safe
		}
	}

	// 3. Try editing placeholder
	if v, loaded := m.placeholders.LoadAndDelete(key); loaded {
		if entry, ok := v.(placeholderEntry); ok && entry.id != "" {
			if editor, ok := ch.(MessageEditor); ok {
				if err := editor.EditMessage(ctx, msg.ChatID, entry.id, msg.Content); err == nil {
					return true // edited successfully, skip Send
				}
				// edit failed → fall through to normal Send
			}
		}
	}

	return false
}

// ManagerOption configures a Manager in NewManager.
type ManagerOption func(*Manager)

// WithBuildHook hands every channel the manager builds (by NewManager or a
// Reload) to f before the channel starts, to inject what the channels
// package cannot provide itself.
func WithBuildHook(f func(name string, ch Channel)) ManagerOption {
	return func(m *Manager) { m.onBuild = f }
}

// NewManager builds the channels cfg asks for. Nothing starts until StartAll.
func NewManager(cfg *config.Config, messageBus *bus.MessageBus, store media.MediaStore, opts ...ManagerOption) (*Manager, error) {
	m := &Manager{
		channels:     make(map[string]Channel),
		workers:      make(map[string]*channelWorker),
		inputs:       make(map[string]string),
		cancels:      make(map[string]context.CancelFunc),
		held:         make(map[string]*heldOutbound),
		startCancels: make(map[string]context.CancelFunc),
		specs:        make(map[string]channelSpec),
		bus:          messageBus,
		mediaStore:   store,
	}
	for _, opt := range opts {
		opt(m)
	}

	logger.InfoC("channels", "Initializing channel manager")
	for _, s := range m.plan(cfg) {
		if ch := m.buildChannel(s); ch != nil {
			m.channels[s.name] = ch
			m.inputs[s.name] = s.inputs
			m.specs[s.name] = s
		}
	}
	logger.InfoCF("channels", "Channel initialization completed", map[string]any{
		"enabled_channels": len(m.channels),
	})
	return m, nil
}

// buildChannel builds the channel s describes with its dependencies, or
// returns nil when the build fails or (a built-in) declines.
func (m *Manager) buildChannel(s channelSpec) Channel {
	logger.DebugCF("channels", "Attempting to initialize channel", map[string]any{"channel": s.display})
	if s.checkAllowFrom {
		warnEmptyAllowFrom(s.display, s.allowFrom)
	}
	ch, err := s.build()
	if err != nil {
		logger.ErrorCF("channels", "Failed to initialize channel", map[string]any{
			"channel": s.display,
			"error":   err.Error(),
		})
		return nil
	}
	if ch == nil {
		return nil
	}
	m.injectChannelDependencies(ch, s.platform)
	if m.onBuild != nil {
		m.onBuild(s.name, ch)
	}
	logger.InfoCF("channels", "Channel enabled successfully", map[string]any{"channel": s.display})
	return ch
}

// injectChannelDependencies injects optional dependencies (platform, MediaStore,
// PlaceholderRecorder, Owner) into a channel if it implements the corresponding
// setter interfaces.
func (m *Manager) injectChannelDependencies(ch Channel, platform string) {
	if setter, ok := ch.(interface{ SetPlatform(p string) }); ok {
		setter.SetPlatform(platform)
	}
	if m.mediaStore != nil {
		if setter, ok := ch.(interface{ SetMediaStore(s media.MediaStore) }); ok {
			setter.SetMediaStore(m.mediaStore)
		}
	}
	if setter, ok := ch.(interface{ SetPlaceholderRecorder(r PlaceholderRecorder) }); ok {
		setter.SetPlaceholderRecorder(m)
	}
	if setter, ok := ch.(interface{ SetOwner(ch Channel) }); ok {
		setter.SetOwner(ch)
	}
	m.alerterMu.RLock()
	al := m.alerter
	m.alerterMu.RUnlock()
	if al != nil {
		if setter, ok := ch.(interface{ SetAlerter(a alerter.Alerter) }); ok {
			setter.SetAlerter(al)
		}
	}
}

// resolveSecMsgAccounts returns the accounts to bind for a daemon, applying
// daemon-level defaults. Pinned accounts are honored as-is; otherwise ClawEh
// discovers the daemon's linked accounts. ok is false when discovery failed
// (daemon down): the accounts already running for the daemon are then kept,
// and new ones are picked up on a later reload once it is reachable. The
// failure alert is raised once per streak of failures.
func (m *Manager) resolveSecMsgAccounts(cfg config.SecMsgConfig) (accounts []config.SecMsgAccountConfig, ok bool) {
	if len(cfg.Accounts) > 0 {
		out := make([]config.SecMsgAccountConfig, len(cfg.Accounts))
		for i, a := range cfg.Accounts {
			out[i] = cfg.WithDefaults(a)
		}
		return out, true
	}

	discover, found := getSecMsgDiscovery()
	if !found {
		// No discovery registered: fall back to a single auto-selecting channel.
		return []config.SecMsgAccountConfig{cfg.WithDefaults(config.SecMsgAccountConfig{})}, true
	}

	ids, err := discover(context.Background(), cfg.Address)
	if err != nil {
		logger.WarnCF("channels", "SecMsg account discovery failed — running accounts kept, no new ones bound (will retry on reload)", map[string]any{
			"channel": "SecMsg (" + cfg.Name + ")",
			"address": cfg.Address,
			"error":   err.Error(),
		})
		if m.discoveryFailed(cfg.Name, true) {
			m.alert(alerter.Alert{
				Title:       "SecMsg account discovery failed",
				Description: "SecMsg (" + cfg.Name + ") at " + cfg.Address + ": accounts already running keep running; others are bound at the next config save",
				Details:     err.Error(),
				EventID:     "SecMsg (" + cfg.Name + ")",
			})
		}
		return nil, false
	}
	m.discoveryFailed(cfg.Name, false)
	if len(ids) == 0 {
		logger.WarnCF("channels", "SecMsg daemon has no linked accounts — link one via the WebUI", map[string]any{
			"channel": "SecMsg (" + cfg.Name + ")",
			"address": cfg.Address,
		})
		m.alert(alerter.Alert{
			Title:       "SecMsg has no linked accounts",
			Description: "SecMsg (" + cfg.Name + "): link an account in the WebUI",
			EventID:     "SecMsg (" + cfg.Name + ")",
		})
		return nil, true
	}

	out := make([]config.SecMsgAccountConfig, len(ids))
	for i, id := range ids {
		out[i] = cfg.WithDefaults(config.SecMsgAccountConfig{Account: id})
	}
	logger.InfoCF("channels", "SecMsg accounts discovered", map[string]any{
		"channel":  "SecMsg (" + cfg.Name + ")",
		"address":  cfg.Address,
		"accounts": ids,
	})
	return out, true
}

// discoveryFailed records whether discovery for daemon name is failing and
// reports whether this starts a streak of failures.
func (m *Manager) discoveryFailed(name string, failing bool) bool {
	m.discoveryMu.Lock()
	defer m.discoveryMu.Unlock()
	if m.discoveryFailing == nil {
		m.discoveryFailing = map[string]bool{}
	}
	was := m.discoveryFailing[name]
	if failing {
		m.discoveryFailing[name] = true
	} else {
		delete(m.discoveryFailing, name)
	}
	return failing && !was
}

// warnEmptyAllowFrom logs a warning when a channel is enabled with an empty allow_from list.
// An empty list now means nobody is allowed — this is almost certainly a misconfiguration.
func warnEmptyAllowFrom(displayName string, allowFrom []string) {
	if len(allowFrom) == 0 {
		logger.WarnCF("channels", "Channel enabled with empty allow_from — nobody will be able to connect. Add user IDs or \"*\" to allow all.",
			map[string]any{"channel": displayName})
	}
}

// RegisterHandlers registers channel webhook handlers, channel health
// endpoints, and the supplied health.Server's /health and /ready endpoints on
// the given mux. The Manager does NOT own the listener: the gateway-level
// httpHost owns the listener and swaps mux instances across config reloads, so
// in-flight WebUI WebSocket connections and other long-lived HTTP traffic
// survive a reload.
func (m *Manager) RegisterHandlers(mux *http.ServeMux, healthServer *health.Server) {
	if healthServer != nil {
		healthServer.RegisterOnMux(mux)
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	for name, ch := range m.channels {
		if wh, ok := ch.(WebhookHandler); ok {
			mux.Handle(wh.WebhookPath(), wh)
			logger.InfoCF("channels", "Webhook handler registered", map[string]any{
				"channel": name,
				"path":    wh.WebhookPath(),
			})
		}
		if hc, ok := ch.(HealthChecker); ok {
			mux.HandleFunc(hc.HealthPath(), hc.HealthHandler)
			logger.InfoCF("channels", "Health endpoint registered", map[string]any{
				"channel": name,
				"path":    hc.HealthPath(),
			})
		}
	}
}

// newChannelWorker creates a channelWorker with a rate limiter configured
// for the given channel name.
func newChannelWorker(name string, ch Channel) *channelWorker {
	rateVal := float64(defaultRateLimit)
	if r, ok := channelRateConfig[name]; ok {
		rateVal = r
	} else {
		// Named channel variants (e.g. "telegram-alice") inherit the base channel's rate.
		for prefix, r := range channelRateConfig {
			if strings.HasPrefix(name, prefix+"-") {
				rateVal = r
				break
			}
		}
	}
	burst := int(math.Max(1, math.Ceil(rateVal/2)))

	return &channelWorker{
		ch:         ch,
		queue:      make(chan bus.OutboundMessage, defaultChannelQueueSize),
		mediaQueue: make(chan bus.OutboundMediaMessage, defaultChannelQueueSize),
		done:       make(chan struct{}),
		mediaDone:  make(chan struct{}),
		limiter:    rate.NewLimiter(rate.Limit(rateVal), burst),
	}
}

// runWorker processes outbound messages for a single channel, splitting
// messages that exceed the channel's maximum message length.
func (m *Manager) runWorker(ctx context.Context, name string, w *channelWorker) {
	defer close(w.done)
	backlog := w.backlog
	w.backlog = nil
	for _, msg := range backlog {
		if err := ctx.Err(); err != nil {
			reportDelivery(msg, err)
			continue
		}
		reportDelivery(msg, m.sendSplit(ctx, name, w, msg))
	}
	for {
		select {
		case msg, ok := <-w.queue:
			if !ok {
				return
			}
			reportDelivery(msg, m.sendSplit(ctx, name, w, msg))
		case <-ctx.Done():
			// Messages still queued are not sent: tell their senders.
			for {
				select {
				case msg, ok := <-w.queue:
					if !ok {
						return
					}
					reportDelivery(msg, ctx.Err())
				default:
					return
				}
			}
		}
	}
}

// sendSplit sends msg through w, split into chunks when it exceeds the
// channel's maximum message length, and returns the first non-nil error
// among the chunks. Every chunk is attempted unless the recipient is offline
// or can't be reached.
func (m *Manager) sendSplit(ctx context.Context, name string, w *channelWorker, msg bus.OutboundMessage) error {
	maxLen := 0
	if mlp, ok := w.ch.(MessageLengthProvider); ok {
		maxLen = mlp.MaxMessageLength()
	}
	if maxLen <= 0 || len([]rune(msg.Content)) <= maxLen {
		return m.sendWithRetry(ctx, name, w, msg)
	}
	var first error
	for i, chunk := range SplitMessage(msg.Content, maxLen) {
		chunkMsg := msg
		chunkMsg.Content = chunk
		// Only the first chunk is a reply; the others are normal messages.
		if i > 0 {
			chunkMsg.ReplyToMessageID = ""
		}
		err := m.sendWithRetry(ctx, name, w, chunkMsg)
		if err != nil && first == nil {
			first = err
		}
		// A recipient that is offline or can't be reached takes none of the
		// remaining chunks either.
		if recipientUnavailable(err) {
			break
		}
	}
	return first
}

// reportDelivery tells msg's sender, when it asked (OnDelivery), how the
// delivery ended.
func reportDelivery(msg bus.OutboundMessage, err error) {
	if msg.OnDelivery != nil {
		msg.OnDelivery(err)
	}
}

// sendWithRetry sends a message through the channel with rate limiting and
// retry logic (retrySend), then reports a failure (reportSendFailure).
func (m *Manager) sendWithRetry(ctx context.Context, name string, w *channelWorker, msg bus.OutboundMessage) error {
	// Rate limit: wait for token
	if err := w.limiter.Wait(ctx); err != nil {
		// ctx canceled, shutting down
		return err
	}

	// Pre-send: stop typing and try to edit placeholder
	if m.preSend(ctx, name, msg, w.ch) {
		return nil // placeholder was edited successfully, skip Send
	}

	// One SendProgress across the retries: a channel that splits the message
	// further resumes after the parts it already delivered.
	sendCtx := WithSendProgress(ctx)
	retries, err := m.retrySend(ctx, func() error { return w.ch.Send(sendCtx, msg) })
	if err != nil && ctx.Err() == nil {
		m.reportSendFailure(name, msg.ChatID, "message", err, retries)
	}
	return err
}

// retrySend calls send until it succeeds or fails for good, and returns the
// number of retries made and the last error. Errors are classified:
//   - ErrNotRunning / ErrSendFailed / ErrReceiveOnly / ErrRecipientOffline /
//     ErrRecipientNotFound / ErrUnknownChannel: permanent, no retry
//   - ErrRateLimit: fixed delay retry; one carrying a retry-after
//     (RetryAfterError) waits it plus RetryAfterPadding, and fails at once
//     when it is longer than MaxSendRetryAfter
//   - ErrTemporary / unknown: exponential backoff retry
//
// A ctx that ends while waiting to retry returns ctx.Err().
func (m *Manager) retrySend(ctx context.Context, send func() error) (int, error) {
	for attempt := 0; ; attempt++ {
		err := send()
		if err == nil || permanentSendError(err) || attempt == maxRetries {
			return attempt, err
		}
		delay := rateLimitDelay
		var ra *RetryAfterError
		switch {
		case !errors.Is(err, ErrRateLimit):
			delay = jitter(min(time.Duration(float64(baseBackoff)*math.Pow(2, float64(attempt))), maxBackoff))
		case errors.As(err, &ra) && ra.After > MaxSendRetryAfter:
			return attempt, err
		case errors.As(err, &ra) && ra.After > 0:
			delay = ra.After + RetryAfterPadding
		}
		select {
		case <-m.clk().After(delay):
		case <-ctx.Done():
			return attempt, ctx.Err()
		}
	}
}

// reportSendFailure logs a send (what: "message" or "file") that failed after
// retries retries, and alerts when the failure is an outage rather than a
// state of the recipient or the configuration.
func (m *Manager) reportSendFailure(name, chatID, what string, err error, retries int) {
	// Receive-only rejection is an expected operator choice, not a fault — log it
	// at INFO so it doesn't read as an error.
	if errors.Is(err, ErrReceiveOnly) {
		logger.InfoCF("channels", "Reply suppressed: recipient account is receive-only", map[string]any{
			"channel": name,
			"chat_id": chatID,
			"kind":    what,
			"detail":  err.Error(),
		})
		return
	}

	// A recipient that is offline or does not exist is an expected state,
	// not an outage: logged at WARN, never alerted.
	if recipientUnavailable(err) {
		logger.WarnCF("channels", "Send dropped: "+recipientReason(err), map[string]any{
			"channel": name,
			"chat_id": chatID,
			"kind":    what,
			"error":   err.Error(),
		})
		return
	}

	// All retries exhausted or permanent failure
	logger.ErrorCF("channels", "Send failed", map[string]any{
		"channel": name,
		"chat_id": chatID,
		"kind":    what,
		"error":   err.Error(),
		"retries": retries,
	})
	description := name + ": a " + what + " could not be delivered"
	switch {
	case retries > 0:
		description += " after " + strconv.Itoa(retries) + " retries"
	case errors.Is(err, ErrNotRunning):
		description += " because the channel is not running"
	}
	m.alert(alerter.Alert{
		Title:       "Channel send failed",
		Description: description,
		Details:     err.Error(),
		EventID:     name,
	})
}

// permanentSendError reports whether err ends a send at once, without retry.
func permanentSendError(err error) bool {
	return errors.Is(err, ErrNotRunning) || errors.Is(err, ErrSendFailed) ||
		errors.Is(err, ErrReceiveOnly) || errors.Is(err, ErrUnknownChannel) ||
		recipientUnavailable(err)
}

// recipientUnavailable reports whether err says the recipient is offline or
// does not exist: an expected state, logged at WARN and never alerted.
func recipientUnavailable(err error) bool {
	return errors.Is(err, ErrRecipientOffline) || errors.Is(err, ErrRecipientNotFound)
}

// recipientReason names the recipient state err reports, for a log message.
func recipientReason(err error) string {
	if errors.Is(err, ErrRecipientOffline) {
		return "recipient offline"
	}
	return "recipient not found"
}

func dispatchLoop[M any](
	ctx context.Context,
	m *Manager,
	subscribe func(context.Context) (M, bool),
	getChannel func(M) string,
	queueOf func(*channelWorker) chan M,
	exitedOf func(*channelWorker) <-chan struct{},
	hold func(*heldOutbound, M) (held, firstOverflow bool),
	drop func(M, error),
	startMsg, stopMsg, unknownMsg, noWorkerMsg string,
) {
	logger.InfoC("channels", startMsg)

	for {
		msg, ok := subscribe(ctx)
		if !ok {
			logger.InfoC("channels", stopMsg)
			return
		}

		channel := getChannel(msg)

		// Silently skip internal channels
		if constants.IsInternalChannel(channel) {
			continue
		}

		if !routeOutbound(ctx, m, channel, msg, queueOf, exitedOf, hold, drop, unknownMsg, noWorkerMsg) {
			return
		}
	}
}

// routeOutbound hands msg to channel's worker, or holds it while the channel
// starts, or drops it with the reason. It returns false once ctx has ended.
func routeOutbound[M any](
	ctx context.Context,
	m *Manager,
	channel string,
	msg M,
	queueOf func(*channelWorker) chan M,
	exitedOf func(*channelWorker) <-chan struct{},
	hold func(*heldOutbound, M) (held, firstOverflow bool),
	drop func(M, error),
	unknownMsg, noWorkerMsg string,
) bool {
	for {
		if err := ctx.Err(); err != nil {
			drop(msg, err)
			return false
		}

		m.mu.RLock()
		_, exists := m.channels[channel]
		w := m.workers[channel]
		h := m.held[channel]
		var held, firstOverflow bool
		if h != nil {
			// Under the read lock, so takeHeldLocked (which runs under the
			// write lock) sees it.
			held, firstOverflow = hold(h, msg)
		}
		m.mu.RUnlock()

		switch {
		case h != nil && held:
			return true
		case h != nil:
			if firstOverflow {
				logger.WarnCF("channels", "Too many messages for a channel that is starting; dropping the rest until it has started", map[string]any{
					"channel": channel,
					"held":    maxHeldPerChannel,
				})
			}
			drop(msg, fmt.Errorf("%w: %s is starting and %d messages are already waiting", ErrNotRunning, channel, maxHeldPerChannel))
			return true
		case !exists:
			logger.WarnCF("channels", unknownMsg, map[string]any{"channel": channel})
			drop(msg, fmt.Errorf("%w: %s", ErrUnknownChannel, channel))
			return true
		case w == nil:
			logger.WarnCF("channels", noWorkerMsg, map[string]any{"channel": channel})
			drop(msg, fmt.Errorf("%w: %s has no active worker", ErrNotRunning, channel))
			return true
		}

		err := enqueueOn(ctx, w, queueOf(w), exitedOf(w), msg)
		switch {
		case err == nil:
			return true
		case errors.Is(err, errWorkerClosed):
			// The channel is being stopped or rebuilt and its worker was
			// already replaced in the maps: look again.
			continue
		default:
			drop(msg, err)
			return false
		}
	}
}

func (m *Manager) dispatchOutbound(ctx context.Context) {
	dispatchLoop(
		ctx, m,
		m.bus.SubscribeOutbound,
		func(msg bus.OutboundMessage) string { return msg.Channel },
		func(w *channelWorker) chan bus.OutboundMessage { return w.queue },
		func(w *channelWorker) <-chan struct{} { return w.done },
		holdText,
		reportDelivery,
		"Outbound dispatcher started",
		"Outbound dispatcher stopped",
		"Unknown channel for outbound message",
		"Channel has no active worker, skipping message",
	)
}

func (m *Manager) dispatchOutboundMedia(ctx context.Context) {
	dispatchLoop(
		ctx, m,
		m.bus.SubscribeOutboundMedia,
		func(msg bus.OutboundMediaMessage) string { return msg.Channel },
		func(w *channelWorker) chan bus.OutboundMediaMessage { return w.mediaQueue },
		func(w *channelWorker) <-chan struct{} { return w.mediaDone },
		holdMedia,
		func(bus.OutboundMediaMessage, error) {},
		"Outbound media dispatcher started",
		"Outbound media dispatcher stopped",
		"Unknown channel for outbound media message",
		"Channel has no active worker, skipping media message",
	)
}

// runMediaWorker processes outbound media messages for a single channel.
func (m *Manager) runMediaWorker(ctx context.Context, name string, w *channelWorker) {
	defer close(w.mediaDone)
	backlog := w.mediaBacklog
	w.mediaBacklog = nil
	for _, msg := range backlog {
		if ctx.Err() != nil {
			break
		}
		m.sendMediaWithRetry(ctx, name, w, msg)
	}
	for {
		select {
		case msg, ok := <-w.mediaQueue:
			if !ok {
				return
			}
			m.sendMediaWithRetry(ctx, name, w, msg)
		case <-ctx.Done():
			return
		}
	}
}

// sendMediaWithRetry sends a media message through the channel with rate
// limiting and the same retry and failure reporting as text
// (sendWithRetry). If the channel does not implement MediaSender, it
// silently skips.
func (m *Manager) sendMediaWithRetry(ctx context.Context, name string, w *channelWorker, msg bus.OutboundMediaMessage) {
	ms, ok := w.ch.(MediaSender)
	if !ok {
		logger.DebugCF("channels", "Channel does not support MediaSender, skipping media", map[string]any{
			"channel": name,
		})
		return
	}

	// Rate limit: wait for token
	if err := w.limiter.Wait(ctx); err != nil {
		return
	}

	retries, err := m.retrySend(ctx, func() error { return ms.SendMedia(ctx, msg) })
	if err != nil && ctx.Err() == nil {
		m.reportSendFailure(name, msg.ChatID, "file", err, retries)
	}
}

// runTTLJanitor periodically scans the typingStops and placeholders maps
// and evicts entries that have exceeded their TTL. This prevents memory
// accumulation when outbound paths fail to trigger preSend (e.g. LLM errors).
func (m *Manager) runTTLJanitor(ctx context.Context) {
	ticker := time.NewTicker(janitorInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			m.typingStops.Range(func(key, value any) bool {
				if entry, ok := value.(typingEntry); ok {
					if now.Sub(entry.createdAt) > typingStopTTL {
						if _, loaded := m.typingStops.LoadAndDelete(key); loaded {
							entry.stop() // idempotent, safe
						}
					}
				}
				return true
			})
			m.reactionUndos.Range(func(key, value any) bool {
				if entry, ok := value.(reactionEntry); ok {
					if now.Sub(entry.createdAt) > typingStopTTL {
						if _, loaded := m.reactionUndos.LoadAndDelete(key); loaded {
							entry.undo() // idempotent, safe
						}
					}
				}
				return true
			})
			m.placeholders.Range(func(key, value any) bool {
				if entry, ok := value.(placeholderEntry); ok {
					if now.Sub(entry.createdAt) > placeholderTTL {
						m.placeholders.Delete(key)
					}
				}
				return true
			})
		}
	}
}

func (m *Manager) GetChannel(name string) (Channel, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	channel, ok := m.channels[name]
	return channel, ok
}

func (m *Manager) GetStatus() map[string]any {
	m.mu.RLock()
	defer m.mu.RUnlock()

	status := make(map[string]any)
	for name, channel := range m.channels {
		status[name] = map[string]any{
			"enabled": true,
			"running": channel.IsRunning(),
		}
	}
	return status
}

func (m *Manager) GetEnabledChannels() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	names := make([]string, 0, len(m.channels))
	for name := range m.channels {
		names = append(names, name)
	}
	return names
}

// Channel returns the enabled channel registered under name, if any. Used by the
// gateway wiring to inject late dependencies into a specific channel.
func (m *Manager) Channel(name string) (Channel, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ch, ok := m.channels[name]
	return ch, ok
}

func (m *Manager) RegisterChannel(name string, channel Channel) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.channels[name] = channel
}

func (m *Manager) UnregisterChannel(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	w := m.workers[name]
	delete(m.workers, name)
	delete(m.channels, name)
	delete(m.inputs, name)
	delete(m.specs, name)
	if cancel := m.startCancels[name]; cancel != nil {
		defer cancel()
		delete(m.startCancels, name)
	}
	text, media := m.takeHeldLocked(name)
	defer dropHeld(name, text, media, "was removed")
	if cancel := m.cancels[name]; cancel != nil {
		defer cancel()
		delete(m.cancels, name)
	}
	if w != nil {
		w.closeQueues()
		<-w.done
		<-w.mediaDone
	}
}

// SendMessage sends an outbound message synchronously through the channel
// worker's rate limiter and retry logic. It blocks until the message is
// delivered (or all retries are exhausted), which preserves ordering when
// a subsequent operation depends on the message having been sent. It
// returns the delivery error (nil once delivered): an unknown channel
// (ErrUnknownChannel), a channel with no worker (ErrNotRunning), or the
// send's own failure after its retries. The
// same result goes to msg.OnDelivery.
func (m *Manager) SendMessage(ctx context.Context, msg bus.OutboundMessage) error {
	m.mu.RLock()
	_, exists := m.channels[msg.Channel]
	w, wExists := m.workers[msg.Channel]
	m.mu.RUnlock()

	var err error
	switch {
	case !exists:
		err = fmt.Errorf("%w: %s", ErrUnknownChannel, msg.Channel)
	case !wExists || w == nil:
		err = fmt.Errorf("%w: %s has no active worker", ErrNotRunning, msg.Channel)
	default:
		err = m.sendSplit(ctx, msg.Channel, w, msg)
	}
	reportDelivery(msg, err)
	return err
}

func (m *Manager) SendToChannel(ctx context.Context, channelName, chatID, content string) error {
	m.mu.RLock()
	_, exists := m.channels[channelName]
	w, wExists := m.workers[channelName]
	m.mu.RUnlock()

	if !exists {
		return fmt.Errorf("%w: %s", ErrUnknownChannel, channelName)
	}

	msg := bus.OutboundMessage{
		Channel: channelName,
		ChatID:  chatID,
		Content: content,
	}

	if wExists && w != nil {
		err := enqueueOn(ctx, w, w.queue, w.done, msg)
		if !errors.Is(err, errWorkerClosed) {
			return err
		}
	}

	// Fallback: direct send (no worker yet, or it is being replaced)
	m.mu.RLock()
	channel := m.channels[channelName]
	m.mu.RUnlock()
	if channel == nil {
		return fmt.Errorf("%w: %s", ErrUnknownChannel, channelName)
	}
	return channel.Send(ctx, msg)
}
