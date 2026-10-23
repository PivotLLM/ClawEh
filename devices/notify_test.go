// ClawEh
// License: MIT

package devices

import (
	"context"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/devices/events"
)

// notifyOnce sends one USB event through a service whose delivery target is
// target and returns the outbound message it published, if any.
func notifyOnce(t *testing.T, target TargetFunc) (bus.OutboundMessage, bool) {
	t.Helper()
	msgBus := bus.NewMessageBus()
	t.Cleanup(msgBus.Close)
	s := NewService(Config{Enabled: true, Target: target})
	s.SetBus(msgBus)

	s.sendNotification(context.Background(), &events.DeviceEvent{
		Action: events.ActionAdd, Kind: events.KindUSB, Vendor: "Acme", Product: "Stick",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	return msgBus.SubscribeOutbound(ctx)
}

// A device notification goes to the target the service is given: the default
// agent's default channel.
func TestSendNotification_UsesTarget(t *testing.T) {
	msg, ok := notifyOnce(t, func() (string, string, bool) { return "telegram", "12345", true })
	if !ok {
		t.Fatal("no notification published")
	}
	if msg.Channel != "telegram" || msg.ChatID != "12345" {
		t.Fatalf("notification went to %s/%s, want telegram/12345", msg.Channel, msg.ChatID)
	}
}

// Without a default channel (none configured, no target, or an internal
// channel) the notification is skipped.
func TestSendNotification_SkipsWithoutTarget(t *testing.T) {
	tests := map[string]TargetFunc{
		"no target":        nil,
		"none configured":  func() (string, string, bool) { return "", "", false },
		"internal channel": func() (string, string, bool) { return "cli", "direct", true },
	}
	for name, target := range tests {
		t.Run(name, func(t *testing.T) {
			if msg, ok := notifyOnce(t, target); ok {
				t.Fatalf("published %+v, want nothing", msg)
			}
		})
	}
}
