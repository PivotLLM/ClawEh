package telegram

import (
	"sync"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/config"
)

// collector captures dispatched messages in a goroutine-safe way and signals
// each dispatch over a channel so tests can wait deterministically.
type collector struct {
	mu   sync.Mutex
	msgs []coalescedMessage
	ch   chan struct{}
}

func newCollector() *collector {
	return &collector{ch: make(chan struct{}, 64)}
}

func (c *collector) dispatch(m coalescedMessage) {
	c.mu.Lock()
	c.msgs = append(c.msgs, m)
	c.mu.Unlock()
	c.ch <- struct{}{}
}

func (c *collector) get() []coalescedMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]coalescedMessage, len(c.msgs))
	copy(out, c.msgs)
	return out
}

func (c *collector) waitFor(t *testing.T, n int) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		c.mu.Lock()
		got := len(c.msgs)
		c.mu.Unlock()
		if got >= n {
			return
		}
		select {
		case <-c.ch:
		case <-deadline:
			t.Fatalf("timed out waiting for %d dispatches, got %d", n, got)
		}
	}
}

func testCoalescer(cfg config.CoalesceConfig, dispatch func(coalescedMessage)) *messageCoalescer {
	return newMessageCoalescer(cfg, dispatch)
}

func msg(id int, chat, sender, content string) coalescedMessage {
	return coalescedMessage{messageID: id, chatID: chat, platformID: sender, content: content}
}

func TestCoalescer_CombinesAfterWindow(t *testing.T) {
	c := newCollector()
	mc := testCoalescer(config.CoalesceConfig{WindowMS: 30}, c.dispatch)

	key := coalesceKey(msg(1, "chat", "user", ""))
	mc.add(key, msg(1, "chat", "user", "para one"))
	mc.add(key, msg(2, "chat", "user", "para two"))
	mc.add(key, msg(3, "chat", "user", "para three"))

	c.waitFor(t, 1)
	got := c.get()
	if len(got) != 1 {
		t.Fatalf("expected 1 combined dispatch, got %d", len(got))
	}
	want := "para one\npara two\npara three"
	if got[0].content != want {
		t.Fatalf("content = %q, want %q", got[0].content, want)
	}
	// Anchored on the last fragment.
	if got[0].messageID != 3 {
		t.Fatalf("messageID = %d, want 3 (last fragment)", got[0].messageID)
	}
}

func TestCoalescer_SortsByMessageID(t *testing.T) {
	c := newCollector()
	mc := testCoalescer(config.CoalesceConfig{WindowMS: 30}, c.dispatch)

	key := coalesceKey(msg(1, "chat", "user", ""))
	// Enqueue out of order, as concurrent handler goroutines might.
	mc.add(key, msg(3, "chat", "user", "third"))
	mc.add(key, msg(1, "chat", "user", "first"))
	mc.add(key, msg(2, "chat", "user", "second"))

	c.waitFor(t, 1)
	got := c.get()[0]
	if got.content != "first\nsecond\nthird" {
		t.Fatalf("content = %q, want ordered by messageID", got.content)
	}
}

func TestCoalescer_ResetsTimerOnNewMessage(t *testing.T) {
	c := newCollector()
	mc := testCoalescer(config.CoalesceConfig{WindowMS: 60}, c.dispatch)
	key := coalesceKey(msg(1, "chat", "user", ""))

	mc.add(key, msg(1, "chat", "user", "a"))
	time.Sleep(30 * time.Millisecond) // less than window
	mc.add(key, msg(2, "chat", "user", "b"))

	// After the first window would have elapsed (60ms from first add), nothing
	// should have fired yet because the second add reset the timer.
	time.Sleep(45 * time.Millisecond)
	if got := len(c.get()); got != 0 {
		t.Fatalf("expected no dispatch before reset window elapsed, got %d", got)
	}

	c.waitFor(t, 1)
	if got := c.get()[0].content; got != "a\nb" {
		t.Fatalf("content = %q, want %q", got, "a\nb")
	}
}

func TestCoalescer_FlushesAtMaxCount(t *testing.T) {
	c := newCollector()
	// Long window, but cap at 3 messages.
	mc := testCoalescer(config.CoalesceConfig{WindowMS: 10000, MaxMessages: 3}, c.dispatch)
	key := coalesceKey(msg(1, "chat", "user", ""))

	mc.add(key, msg(1, "chat", "user", "a"))
	mc.add(key, msg(2, "chat", "user", "b"))
	mc.add(key, msg(3, "chat", "user", "c")) // hits cap -> immediate flush

	c.waitFor(t, 1)
	got := c.get()
	if len(got) != 1 || got[0].content != "a\nb\nc" {
		t.Fatalf("expected immediate combined flush at cap, got %+v", got)
	}
}

func TestCoalescer_FlushesAtMaxWait(t *testing.T) {
	c := newCollector()
	mc := testCoalescer(config.CoalesceConfig{WindowMS: 10000, MaxWaitMS: 40}, c.dispatch)
	key := coalesceKey(msg(1, "chat", "user", ""))

	base := time.Now()
	mc.now = func() time.Time { return base }
	mc.add(key, msg(1, "chat", "user", "a"))
	// Advance the clock past the max-wait ceiling; the next add flushes.
	mc.now = func() time.Time { return base.Add(50 * time.Millisecond) }
	mc.add(key, msg(2, "chat", "user", "b"))

	c.waitFor(t, 1)
	if got := c.get()[0].content; got != "a\nb" {
		t.Fatalf("content = %q, want %q", got, "a\nb")
	}
}

func TestCoalescer_DoesNotMergeDifferentSenders(t *testing.T) {
	c := newCollector()
	mc := testCoalescer(config.CoalesceConfig{WindowMS: 30}, c.dispatch)

	mc.add(coalesceKey(msg(1, "chat", "alice", "")), msg(1, "chat", "alice", "from alice"))
	mc.add(coalesceKey(msg(2, "chat", "bob", "")), msg(2, "chat", "bob", "from bob"))

	c.waitFor(t, 2)
	got := c.get()
	if len(got) != 2 {
		t.Fatalf("expected 2 separate dispatches, got %d", len(got))
	}
	seen := map[string]bool{}
	for _, m := range got {
		seen[m.content] = true
	}
	if !seen["from alice"] || !seen["from bob"] {
		t.Fatalf("senders were merged or lost: %+v", got)
	}
}

func TestCoalescer_FlushAll(t *testing.T) {
	c := newCollector()
	mc := testCoalescer(config.CoalesceConfig{WindowMS: 10000}, c.dispatch)

	mc.add(coalesceKey(msg(1, "chatA", "user", "")), msg(1, "chatA", "user", "a"))
	mc.add(coalesceKey(msg(2, "chatB", "user", "")), msg(2, "chatB", "user", "b"))

	mc.flushAll()
	c.waitFor(t, 2)
	if got := len(c.get()); got != 2 {
		t.Fatalf("flushAll dispatched %d, want 2", got)
	}
}

func TestCoalescer_FlushKeyEmptyNoop(t *testing.T) {
	c := newCollector()
	mc := testCoalescer(config.CoalesceConfig{WindowMS: 30}, c.dispatch)
	mc.flushKey("nonexistent")
	if got := len(c.get()); got != 0 {
		t.Fatalf("flushKey on empty buffer dispatched %d, want 0", got)
	}
}

func TestCoalesceConfigDefaults(t *testing.T) {
	var cfg config.CoalesceConfig
	if cfg.Window() != config.DefaultCoalesceWindowMS*time.Millisecond {
		t.Fatalf("Window default = %v", cfg.Window())
	}
	if cfg.MaxWait() != config.DefaultCoalesceMaxWaitMS*time.Millisecond {
		t.Fatalf("MaxWait default = %v", cfg.MaxWait())
	}
	if cfg.MaxMessageCount() != config.DefaultCoalesceMaxMessages {
		t.Fatalf("MaxMessageCount default = %d", cfg.MaxMessageCount())
	}
}
