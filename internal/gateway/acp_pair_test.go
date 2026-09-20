package gateway

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/PivotLLM/ClawEh/channels/device"
)

// TestAutoApproveLocalDevice verifies the bridge self-pairs its own pending
// request in the store and leaves the device paired.
func TestAutoApproveLocalDevice(t *testing.T) {
	dataDir := t.TempDir()
	stateDir := filepath.Join(dataDir, "state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	store, err := device.OpenStore(context.Background(), filepath.Join(stateDir, "gateway.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	ctx := context.Background()

	const deviceID = "dev-abc"
	if _, pendErr := store.CreatePending(ctx, device.PendingPairing{
		DeviceID:  deviceID,
		PublicKey: "pubkey-abc",
		Role:      "node",
	}); pendErr != nil {
		t.Fatalf("CreatePending: %v", pendErr)
	}
	// Also enqueue an unrelated device to prove we only approve our own.
	if _, pendErr := store.CreatePending(ctx, device.PendingPairing{
		DeviceID:  "other-device",
		PublicKey: "pubkey-other",
		Role:      "node",
	}); pendErr != nil {
		t.Fatalf("CreatePending other: %v", pendErr)
	}
	if closeErr := store.Close(); closeErr != nil { // the helper opens its own handle
		t.Fatalf("close store: %v", closeErr)
	}

	if _, approveErr := autoApproveLocalDevice(ctx, dataDir, deviceID); approveErr != nil {
		t.Fatalf("autoApproveLocalDevice: %v", approveErr)
	}

	verify, err := device.OpenStore(context.Background(), filepath.Join(stateDir, "gateway.db"))
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer func() {
		if closeErr := verify.Close(); closeErr != nil {
			t.Errorf("close verify store: %v", closeErr)
		}
	}()
	if _, ok, err := verify.GetPaired(ctx, deviceID); err != nil || !ok {
		t.Fatalf("device %s not paired after auto-approve (ok=%v err=%v)", deviceID, ok, err)
	}
	if _, ok, err := verify.GetPaired(ctx, "other-device"); err != nil || ok {
		t.Fatalf("unrelated device was wrongly approved (ok=%v err=%v)", ok, err)
	}
}

// TestAutoApproveLocalDeviceNoPending errors clearly when there is nothing to
// approve for this device.
func TestAutoApproveLocalDeviceNoPending(t *testing.T) {
	dataDir := t.TempDir()
	stateDir := filepath.Join(dataDir, "state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	store, err := device.OpenStore(context.Background(), filepath.Join(stateDir, "gateway.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	if closeErr := store.Close(); closeErr != nil {
		t.Fatalf("close store: %v", closeErr)
	}

	if _, err := autoApproveLocalDevice(context.Background(), dataDir, "missing"); err == nil {
		t.Fatalf("expected error when no pending pairing exists")
	}
}
