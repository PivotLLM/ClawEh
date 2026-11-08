package bus

import (
	"context"
	"errors"
	"testing"
)

// Messages still in the bus's buffer when it closes are reported to senders
// waiting on their delivery (OnDelivery) as never sent.
func TestClose_ReportsBufferedDeliveries(t *testing.T) {
	mb := NewMessageBus()
	var text, media error
	if err := mb.PublishOutbound(context.Background(), OutboundMessage{Channel: "telegram", ChatID: "1", OnDelivery: func(err error) { text = err }}); err != nil {
		t.Fatal(err)
	}
	if err := mb.PublishOutboundMedia(context.Background(), OutboundMediaMessage{Channel: "telegram", ChatID: "1", OnDelivery: func(err error) { media = err }}); err != nil {
		t.Fatal(err)
	}
	mb.Close()
	if !errors.Is(text, ErrBusClosed) || !errors.Is(media, ErrBusClosed) {
		t.Fatalf("reports = %v, %v; want ErrBusClosed for both", text, media)
	}
}
