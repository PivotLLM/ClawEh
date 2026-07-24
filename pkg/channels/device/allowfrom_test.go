package device

import (
	"context"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/bus"
	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/identity"
)

func deviceSender(id string) bus.SenderInfo {
	return bus.SenderInfo{
		Platform:    "device",
		PlatformID:  id,
		CanonicalID: identity.BuildCanonicalID("device", id),
	}
}

// TestDeviceChannelEmptyAllowFromAllowsPairedDevice guards the fix: with no
// allow_from configured, a paired device must NOT be dropped by the sender
// allow-list (the gateway already authenticates via token + pairing). An empty
// allow-list previously meant "deny all", silently dropping every device turn.
func TestDeviceChannelEmptyAllowFromAllowsPairedDevice(t *testing.T) {
	dc, err := NewDeviceChannel(config.DeviceChannelConfig{Enabled: true}, t.TempDir(), false, bus.NewMessageBus())
	if err != nil {
		t.Fatalf("NewDeviceChannel: %v", err)
	}
	defer func() { _ = dc.Stop(context.Background()) }()

	if !dc.IsAllowedSender(deviceSender("dev1")) {
		t.Fatal("empty allow_from should allow a paired device, but the sender was rejected")
	}
}

// TestDeviceChannelExplicitAllowFromRestricts confirms an explicit allow_from
// still restricts: a device not on the list is rejected.
func TestDeviceChannelExplicitAllowFromRestricts(t *testing.T) {
	dc, err := NewDeviceChannel(
		config.DeviceChannelConfig{Enabled: true, AllowFrom: config.FlexibleStringSlice{"device:allowed"}},
		t.TempDir(), false, bus.NewMessageBus(),
	)
	if err != nil {
		t.Fatalf("NewDeviceChannel: %v", err)
	}
	defer func() { _ = dc.Stop(context.Background()) }()

	if dc.IsAllowedSender(deviceSender("someone-else")) {
		t.Fatal("a device not in an explicit allow_from must be rejected")
	}
}
