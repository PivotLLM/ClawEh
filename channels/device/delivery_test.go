// ClawEh
// License: MIT

package device

import (
	"context"
	"errors"
	"path/filepath"
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
// through the manager, with why: no such device, or a paired device that is
// not connected or whose connection is closing.
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
	// A connection that is closing: its writer has already closed.
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
		{"connection closing", "device:broken", channels.ErrRecipientOffline},
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

// A fault of the server itself (the pairing store fails) is ErrSendFailed.
func TestDeliverReply_StoreFaultIsSendFailed(t *testing.T) {
	store, err := OpenStore(context.Background(), filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(store, ServerOptions{ServerVersion: "test-1"})
	if err := srv.DeliverReply(context.Background(), "device:paired-1", "hello"); !errors.Is(err, channels.ErrSendFailed) {
		t.Fatalf("DeliverReply = %v, want ErrSendFailed", err)
	}
}

// A send cut short by shutdown returns the context's error, never a
// delivery reason the manager would alert on.
func TestDeviceSend_ShutdownReturnsContextError(t *testing.T) {
	srv, store, _ := newTestServer(t, ServerOptions{ServerVersion: "test-1"})
	dc := &DeviceChannel{BaseChannel: channels.NewBaseChannel("device", nil, nil, nil), server: srv, store: store}
	dc.SetRunning(true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := dc.Send(ctx, bus.OutboundMessage{Channel: "device", ChatID: "device:nosuch", Content: "hello"})
	if !errors.Is(err, context.Canceled) || errors.Is(err, channels.ErrSendFailed) {
		t.Fatalf("Send = %v, want context.Canceled", err)
	}
}
