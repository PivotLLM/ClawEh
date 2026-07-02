// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package channels

import (
	"context"
	"testing"
)

// streamMockChannel is a mockChannel that also implements StreamCapable,
// recording each delta it receives.
type streamMockChannel struct {
	mockChannel
	deltas []string
}

func (s *streamMockChannel) StreamDelta(_ context.Context, _, delta string) error {
	s.deltas = append(s.deltas, delta)
	return nil
}

func TestSupportsStreaming(t *testing.T) {
	m := newTestManager()
	m.channels["stream"] = &streamMockChannel{}
	m.channels["plain"] = &mockChannel{}

	if !m.SupportsStreaming("stream") {
		t.Error("StreamCapable channel should report SupportsStreaming true")
	}
	if m.SupportsStreaming("plain") {
		t.Error("non-StreamCapable channel should report SupportsStreaming false")
	}
	if m.SupportsStreaming("missing") {
		t.Error("unknown channel should report SupportsStreaming false")
	}
}

func TestStreamDeltaRouting(t *testing.T) {
	m := newTestManager()
	sc := &streamMockChannel{}
	m.channels["stream"] = sc
	m.channels["plain"] = &mockChannel{}

	m.StreamDelta("stream", "chat-1", "hello ")
	m.StreamDelta("stream", "chat-1", "world")
	if len(sc.deltas) != 2 || sc.deltas[0] != "hello " || sc.deltas[1] != "world" {
		t.Fatalf("expected deltas [hello , world] routed to StreamCapable channel, got %v", sc.deltas)
	}

	// Non-capable and unknown channels are no-ops (must not panic).
	m.StreamDelta("plain", "chat-1", "ignored")
	m.StreamDelta("missing", "chat-1", "ignored")
}
