// ClawEh
// License: MIT

package device

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/channels"
	"github.com/PivotLLM/ClawEh/config"
)

// listenerlessDevice is the device channel without its listener: Start and
// Stop do nothing, so the manager can run it in a test.
type listenerlessDevice struct{ *DeviceChannel }

func (listenerlessDevice) Start(context.Context) error { return nil }
func (listenerlessDevice) Stop(context.Context) error  { return nil }

// A reply the device channel cannot deliver reaches the sender's OnDelivery,
// through the manager, with why: no such device, a paired device that is not
// connected, or a failed write.
func TestDeliverReply_ReasonsThroughManager(t *testing.T) {
	srv, store, _ := newTestServer(t, ServerOptions{ServerVersion: "test-1"})
	ctx := context.Background()

	// A paired device that is not connected.
	reqID, err := store.CreatePending(ctx, PendingPairing{DeviceID: "paired-1", PublicKey: "pk", Role: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.Approve(ctx, reqID, []string{"operator"}, nil); err != nil {
		t.Fatal(err)
	}
	// A connection whose writer has already closed.
	cw := &connWriter{queue: make(chan any, 1), done: make(chan struct{})}
	close(cw.done)
	srv.conns.Store("device:broken", &liveConn{cw: cw, deviceID: "broken", chatID: "device:broken"})

	dc := &DeviceChannel{BaseChannel: channels.NewBaseChannel("device", nil, nil, nil), server: srv, store: store}
	dc.SetRunning(true)
	mb := bus.NewMessageBus()
	t.Cleanup(mb.Close)
	m, err := channels.NewManager(&config.Config{}, mb, nil)
	if err != nil {
		t.Fatal(err)
	}
	m.RegisterChannel("device", listenerlessDevice{dc})
	if err := m.StartAll(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := m.StopAll(context.Background()); err != nil {
			t.Errorf("StopAll: %v", err)
		}
	})

	tests := []struct {
		name   string
		chatID string
		want   error
	}{
		{"unknown device", "device:nosuch", channels.ErrRecipientNotFound},
		{"not a device chat", "telegram:1", channels.ErrRecipientNotFound},
		{"not connected", "device:paired-1", channels.ErrRecipientOffline},
		{"write error", "device:broken", channels.ErrSendFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := make(chan error, 1)
			err := mb.PublishOutbound(ctx, bus.OutboundMessage{
				Channel: "device", ChatID: tt.chatID, Content: "hello",
				OnDelivery: func(err error) { got <- err },
			})
			if err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-got:
				if !errors.Is(err, tt.want) {
					t.Fatalf("delivery error = %v, want %v", err, tt.want)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("OnDelivery was not called")
			}
		})
	}
}
