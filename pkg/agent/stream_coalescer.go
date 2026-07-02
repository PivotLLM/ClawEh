// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package agent

import (
	"strings"
	"sync"
)

// streamFlushThreshold is the buffered-rune count at which the coalescer flushes
// even without hitting a sentence boundary. Token deltas arrive as tiny
// fragments; batching to a sentence or ~this many chars keeps the device gateway
// from emitting a WebSocket frame per token while still feeling live.
const streamFlushThreshold = 120

// streamCoalescer batches per-token text deltas from a provider and forwards
// coalesced chunks to a sink on sentence boundaries or a length cap, whichever
// comes first. Add may be called from the provider's goroutine while the turn
// flushes from another, so all state is guarded by a mutex.
type streamCoalescer struct {
	mu   sync.Mutex
	buf  strings.Builder
	sink func(string)
}

// newStreamCoalescer builds a coalescer that forwards batched text to sink. sink
// must be safe to call from the goroutine that invokes Add/Flush.
func newStreamCoalescer(sink func(string)) *streamCoalescer {
	return &streamCoalescer{sink: sink}
}

// Add appends a token delta and flushes the accumulated buffer when it ends at a
// sentence boundary (. ! ? or newline) or exceeds streamFlushThreshold runes.
func (c *streamCoalescer) Add(delta string) {
	if delta == "" {
		return
	}
	c.mu.Lock()
	c.buf.WriteString(delta)
	if !endsAtBoundary(delta) && c.buf.Len() < streamFlushThreshold {
		c.mu.Unlock()
		return
	}
	batch := c.buf.String()
	c.buf.Reset()
	c.mu.Unlock()
	if batch != "" {
		c.sink(batch)
	}
}

// Flush emits any buffered remainder. Safe to call multiple times (a no-op once
// the buffer is empty); the turn defers it to release trailing text that never
// hit a boundary.
func (c *streamCoalescer) Flush() {
	c.mu.Lock()
	batch := c.buf.String()
	c.buf.Reset()
	c.mu.Unlock()
	if batch != "" {
		c.sink(batch)
	}
}

// endsAtBoundary reports whether s ends with a sentence-terminating rune or a
// newline, the signal to flush a coalesced batch.
func endsAtBoundary(s string) bool {
	if s == "" {
		return false
	}
	switch s[len(s)-1] {
	case '.', '!', '?', '\n':
		return true
	default:
		return false
	}
}
