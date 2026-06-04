package telegram

import (
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/bus"
	"github.com/PivotLLM/ClawEh/pkg/config"
)

// coalescedMessage holds the fully-parsed arguments for one inbound Telegram
// message, captured at the point handleMessage would otherwise have called
// HandleMessage. Buffered fragments from the same sender+chat are combined into
// a single coalescedMessage before being dispatched.
type coalescedMessage struct {
	messageID  int
	peer       bus.Peer
	platformID string
	chatID     string
	content    string
	media      []string
	metadata   map[string]string
	sender     bus.SenderInfo
}

type coalesceBuffer struct {
	msgs    []coalescedMessage
	timer   *time.Timer
	firstAt time.Time
}

// messageCoalescer buffers consecutive messages keyed by sender+chat and
// flushes them as one combined message after a quiet period. Telegram (notably
// its mobile app) splits a single long paste into multiple messages; without
// coalescing each fragment becomes its own agent turn, so the model answers a
// partial instruction and then sees its own reply before the next fragment.
//
// All buffer state is guarded by mu; per-key timers are created with
// time.AfterFunc and always extracted-and-stopped under the lock, so a timer
// that fires concurrently with an explicit flush or a new message is a no-op
// once the buffer has been taken.
type messageCoalescer struct {
	window   time.Duration
	maxWait  time.Duration
	maxCount int
	dispatch func(coalescedMessage)

	mu      sync.Mutex
	buffers map[string]*coalesceBuffer
	now     func() time.Time // injectable clock for tests
}

func newMessageCoalescer(cfg config.CoalesceConfig, dispatch func(coalescedMessage)) *messageCoalescer {
	return &messageCoalescer{
		window:   cfg.Window(),
		maxWait:  cfg.MaxWait(),
		maxCount: cfg.MaxMessageCount(),
		dispatch: dispatch,
		buffers:  make(map[string]*coalesceBuffer),
		now:      time.Now,
	}
}

// add appends a message to the buffer for key and (re)arms the quiet-period
// timer. It flushes immediately if the buffer hits the message-count cap or the
// max-wait ceiling from its first message.
func (mc *messageCoalescer) add(key string, msg coalescedMessage) {
	mc.mu.Lock()
	b := mc.buffers[key]
	if b == nil {
		b = &coalesceBuffer{firstAt: mc.now()}
		mc.buffers[key] = b
	}
	b.msgs = append(b.msgs, msg)
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}

	if len(b.msgs) >= mc.maxCount || mc.now().Sub(b.firstAt) >= mc.maxWait {
		combined := mc.takeLocked(key)
		mc.mu.Unlock()
		mc.dispatchIf(combined)
		return
	}

	b.timer = time.AfterFunc(mc.window, func() { mc.flushKey(key) })
	mc.mu.Unlock()
}

// flushKey combines and dispatches the buffer for key, if any. Safe to call
// from a timer goroutine or directly; concurrent callers race on takeLocked and
// only one wins.
func (mc *messageCoalescer) flushKey(key string) {
	mc.mu.Lock()
	combined := mc.takeLocked(key)
	mc.mu.Unlock()
	mc.dispatchIf(combined)
}

// flushAll combines and dispatches every buffered key. Used on shutdown so
// buffered user input is not silently dropped.
func (mc *messageCoalescer) flushAll() {
	mc.mu.Lock()
	combined := make([]*coalescedMessage, 0, len(mc.buffers))
	for key := range mc.buffers {
		combined = append(combined, mc.takeLocked(key))
	}
	mc.mu.Unlock()
	for _, c := range combined {
		mc.dispatchIf(c)
	}
}

func (mc *messageCoalescer) dispatchIf(m *coalescedMessage) {
	if m != nil {
		mc.dispatch(*m)
	}
}

// takeLocked removes the buffer for key, stops its timer, and returns the
// combined message (nil if there was no buffer). Caller must hold mc.mu.
func (mc *messageCoalescer) takeLocked(key string) *coalescedMessage {
	b := mc.buffers[key]
	if b == nil {
		return nil
	}
	delete(mc.buffers, key)
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}
	return combineMessages(b.msgs)
}

// combineMessages reassembles buffered fragments into a single message. It
// sorts by Telegram MessageID (monotonic per chat) so fragments enqueued out of
// order by concurrent handler goroutines are reassembled in send order, then
// concatenates their content and media. The combined message is anchored on the
// last fragment so the reply and media scope attach to the end of the paste.
func combineMessages(msgs []coalescedMessage) *coalescedMessage {
	if len(msgs) == 0 {
		return nil
	}
	sort.SliceStable(msgs, func(i, j int) bool {
		return msgs[i].messageID < msgs[j].messageID
	})
	if len(msgs) == 1 {
		return &msgs[0]
	}

	out := msgs[len(msgs)-1]
	parts := make([]string, 0, len(msgs))
	var media []string
	for _, m := range msgs {
		if m.content != "" {
			parts = append(parts, m.content)
		}
		media = append(media, m.media...)
	}
	out.content = strings.Join(parts, "\n")
	out.media = media
	return &out
}

// coalesceKey returns the buffer key for a message: messages are coalesced only
// when they share both chat (including forum thread) and sender, so distinct
// users in a group are never merged.
func coalesceKey(m coalescedMessage) string {
	return m.chatID + "\x00" + m.platformID
}
