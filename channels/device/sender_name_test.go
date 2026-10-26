package device

import (
	"context"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
)

// A device message reaches the bus with the device's user as the sender's
// display name: the paired device's user name, else the default user (the
// admin account), else the device's display name. Username stays empty.
func TestInbound_SenderDisplayName(t *testing.T) {
	for _, tc := range []struct {
		name, userName, defaultUser, want string
	}{
		{"stored user name", "Alice", "bob", "Alice"},
		{"default user", "", "bob", "bob"},
		{"device display name", "", "", "Rabbit R1"},
		{"blank default user", "", "  ", "Rabbit R1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mb := bus.NewMessageBus()
			defer mb.Close()
			dc, err := NewDeviceChannel(config.DeviceChannelConfig{Enabled: true}, t.TempDir(), false, mb, "", nil)
			if err != nil {
				t.Fatalf("NewDeviceChannel: %v", err)
			}
			defer func() {
				if stopErr := dc.Stop(context.Background()); stopErr != nil {
					t.Errorf("Stop: %v", stopErr)
				}
			}()
			pairStoreDevice(t, dc.store, "dev1")
			if tc.userName != "" {
				if err := dc.store.SetDeviceUser(context.Background(), "dev1", tc.userName); err != nil {
					t.Fatalf("SetDeviceUser: %v", err)
				}
			}
			dc.SetDefaultUser(func() string { return tc.defaultUser })

			dc.server.inbound("dev1", "device:dev1", "hello", "run1", "", nil)

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			msg, ok := mb.ConsumeInbound(ctx)
			if !ok {
				t.Fatal("no inbound message on the bus")
			}
			if msg.Sender.DisplayName != tc.want || msg.Sender.Username != "" {
				t.Fatalf("sender = %+v, want DisplayName %q and no Username", msg.Sender, tc.want)
			}
		})
	}
}
