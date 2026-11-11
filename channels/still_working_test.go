// ClawEh
// License: MIT

package channels

import (
	"context"
	"fmt"
	"testing"

	"golang.org/x/time/rate"

	"github.com/PivotLLM/ClawEh/bus"
)

// workingChannel is a channel with a typing indicator, placeholders and
// message edits that records each of them.
type workingChannel struct {
	BaseChannel
	typingStarts, typingStops int
	placeholders              int
	edits                     []string // "placeholderID=content"
	sent                      []string
}

func (c *workingChannel) Start(context.Context) error { return nil }
func (c *workingChannel) Stop(context.Context) error  { return nil }
func (c *workingChannel) Send(_ context.Context, msg bus.OutboundMessage) error {
	c.sent = append(c.sent, msg.Content)
	return nil
}

func (c *workingChannel) StartTyping(context.Context, string) (func(), error) {
	c.typingStarts++
	return func() { c.typingStops++ }, nil
}

func (c *workingChannel) SendPlaceholder(context.Context, string) (string, error) {
	c.placeholders++
	return fmt.Sprintf("ph%d", c.placeholders), nil
}

func (c *workingChannel) EditMessage(_ context.Context, _, messageID, content string) error {
	c.edits = append(c.edits, messageID+"="+content)
	return nil
}

// TestStillWorking_ShowsTheChatBusyAgain: an interim message sent while the
// turn goes on clears the typing indicator and replaces the placeholder like
// any message, then both are shown again until the reply clears them; a chat
// that showed neither gets neither, and an ordinary message restores nothing.
func TestStillWorking_ShowsTheChatBusyAgain(t *testing.T) {
	for _, tc := range []struct {
		name         string
		busy         bool // the chat shows typing and a placeholder before
		stillWorking bool
		wantTyping   int // typing starts after the interim message
		wantPh       int // placeholders sent after the interim message
	}{
		{name: "busy chat", busy: true, stillWorking: true, wantTyping: 1, wantPh: 1},
		{name: "idle chat", stillWorking: true},
		{name: "ordinary message", busy: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestManager()
			ch := &workingChannel{}
			w := &channelWorker{ch: ch, limiter: rate.NewLimiter(rate.Inf, 1)}
			if tc.busy {
				m.RecordTypingStop("test", "1", func() { ch.typingStops++ })
				m.RecordPlaceholder("test", "1", "ph0")
			}

			notice := bus.OutboundMessage{Channel: "test", ChatID: "1", Content: "Alice is summarising", StillWorking: tc.stillWorking}
			if err := m.sendSplit(context.Background(), "test", w, notice); err != nil {
				t.Fatalf("send the notice: %v", err)
			}
			if tc.busy && (ch.typingStops != 1 || len(ch.edits) != 1 || ch.edits[0] != "ph0=Alice is summarising") {
				t.Fatalf("the notice did not take the chat's indicator: typing stops %d, edits %v", ch.typingStops, ch.edits)
			}
			if ch.typingStarts != tc.wantTyping || ch.placeholders != tc.wantPh {
				t.Fatalf("after the notice: typing started %d times, %d placeholders; want %d, %d",
					ch.typingStarts, ch.placeholders, tc.wantTyping, tc.wantPh)
			}
			_, typing := m.typingStops.Load("test:1")
			_, placeholder := m.placeholders.Load("test:1")
			if busyAgain := tc.wantTyping > 0; typing != busyAgain || placeholder != busyAgain {
				t.Fatalf("after the notice: typing recorded %v, placeholder recorded %v; want %v", typing, placeholder, busyAgain)
			}
			if tc.wantTyping == 0 {
				return
			}

			// The reply ends what the notice brought back.
			stopsBefore := ch.typingStops
			if err := m.sendSplit(context.Background(), "test", w, bus.OutboundMessage{Channel: "test", ChatID: "1", Content: "hello"}); err != nil {
				t.Fatalf("send the reply: %v", err)
			}
			if ch.typingStops != stopsBefore+1 {
				t.Errorf("the reply did not stop the typing indicator")
			}
			if last := ch.edits[len(ch.edits)-1]; last != "ph1=hello" {
				t.Errorf("last edit = %q, want the new placeholder replaced with the reply", last)
			}
			if _, ok := m.typingStops.Load("test:1"); ok {
				t.Error("typing still recorded after the reply")
			}
		})
	}
}
