// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package agent

import (
	"strings"
	"sync"
)

// streamToolNarration controls whether partial assistant text is streamed to
// streaming-capable channels (the device gateway / R1) as the model generates.
//
// When true, every iteration's assistant text is streamed live — including any
// narration the model emits before a tool call ("Sure, let me check…"), which a
// voice device will speak. When false, partial streaming is disabled entirely
// and the full reply is delivered once at the end (via the normal terminal
// event), so no intermediate narration is spoken.
//
// It's an all-or-nothing switch on purpose: once a chunk is streamed it has
// already been spoken, so there's no clean way to keep live streaming yet
// suppress only the pre-tool narration. Flip this to false to turn partial
// streaming off, or lift it into a config option, if the narration is unwanted.
const streamToolNarration = true

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

// Add appends a token delta and flushes the accumulated buffer at a good break
// point (see shouldFlush). A whitespace-only buffer is never flushed on its own —
// it stays buffered to prepend the next real text, so no empty/blank chunks are
// emitted.
func (c *streamCoalescer) Add(delta string) {
	if delta == "" {
		return
	}
	c.mu.Lock()
	c.buf.WriteString(delta)
	s := c.buf.String()
	if !shouldFlush(s) || strings.TrimSpace(s) == "" {
		c.mu.Unlock()
		return
	}
	c.buf.Reset()
	c.mu.Unlock()
	c.sink(s)
}

// Flush emits any buffered remainder (skipping a whitespace-only tail). Safe to
// call multiple times; the turn defers it to release trailing text that never hit
// a boundary.
func (c *streamCoalescer) Flush() {
	c.mu.Lock()
	batch := c.buf.String()
	c.buf.Reset()
	c.mu.Unlock()
	if strings.TrimSpace(batch) != "" {
		c.sink(batch)
	}
}

// shouldFlush reports whether the buffered text is at a good place to emit: a
// newline, the length cap, or a real sentence end. A trailing "." preceded by a
// digit is treated as a list marker / decimal ("1.", "3.14") — NOT a sentence
// end — so the number is not split from the text that follows it (which the
// device would speak as a separate utterance, causing an unnatural pause).
func shouldFlush(s string) bool {
	n := len(s)
	if n == 0 {
		return false
	}
	if s[n-1] == '\n' {
		return true
	}
	if n >= streamFlushThreshold {
		return true
	}
	switch s[n-1] {
	case '.':
		return !(n >= 2 && s[n-2] >= '0' && s[n-2] <= '9')
	case '!', '?':
		return true
	}
	return false
}
