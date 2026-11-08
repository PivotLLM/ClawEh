package channels

import (
	"errors"
	"testing"

	"golang.org/x/time/rate"

	"github.com/PivotLLM/ClawEh/bus"
)

// The media worker reports how each file's delivery ended: nil when sent,
// the channel's reason when it gave up, ErrNoFileSupport for a channel that
// cannot send files.
func TestRunMediaWorker_ReportsDelivery(t *testing.T) {
	tests := []struct {
		name string
		ch   Channel
		want error
	}{
		{name: "delivered", ch: &mediaMock{}, want: nil},
		{name: "send failed", ch: &mediaMock{err: ErrSendFailed}, want: ErrSendFailed},
		{name: "no file support", ch: &mockChannel{}, want: ErrNoFileSupport},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTestManager()
			w := &channelWorker{
				ch: tt.ch, mediaQueue: make(chan bus.OutboundMediaMessage, 1), mediaDone: make(chan struct{}),
				limiter: rate.NewLimiter(rate.Inf, 1),
			}
			go m.runMediaWorker(t.Context(), "test", w)

			reports, onDelivery := deliveryReport()
			w.mediaQueue <- bus.OutboundMediaMessage{Channel: "test", ChatID: "1", OnDelivery: onDelivery}
			err := expectDelivery(t, reports)
			if tt.want == nil {
				if err != nil {
					t.Fatalf("delivery error = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tt.want) {
				t.Fatalf("delivery error = %v, want %v", err, tt.want)
			}
		})
	}
}

// Files the dispatcher cannot hand to any channel are reported as failed.
func TestDispatchOutboundMedia_ReportsUnknownChannel(t *testing.T) {
	mb := bus.NewMessageBus()
	defer mb.Close()
	m := &Manager{channels: make(map[string]Channel), workers: make(map[string]*channelWorker), bus: mb}
	go m.dispatchOutboundMedia(t.Context())

	reports, onDelivery := deliveryReport()
	if err := mb.PublishOutboundMedia(t.Context(), bus.OutboundMediaMessage{Channel: "nowhere", ChatID: "1", OnDelivery: onDelivery}); err != nil {
		t.Fatal(err)
	}
	if err := expectDelivery(t, reports); !errors.Is(err, ErrUnknownChannel) {
		t.Fatalf("delivery error = %v, want ErrUnknownChannel", err)
	}
}

// Files held for a channel that then does not start are reported as failed.
func TestDropHeld_ReportsMedia(t *testing.T) {
	reports, onDelivery := deliveryReport()
	dropHeld("test", nil, []bus.OutboundMediaMessage{{Channel: "test", ChatID: "1", OnDelivery: onDelivery}}, "did not start")
	if err := expectDelivery(t, reports); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("delivery error = %v, want ErrNotRunning", err)
	}
}

// SupportsMedia is true only for a configured channel that can send files.
func TestManager_SupportsMedia(t *testing.T) {
	m := &Manager{channels: map[string]Channel{"files": &mediaMock{}, "text": &mockChannel{}}}
	for name, want := range map[string]bool{"files": true, "text": false, "nowhere": false} {
		if got := m.SupportsMedia(name); got != want {
			t.Errorf("SupportsMedia(%q) = %v, want %v", name, got, want)
		}
	}
}
