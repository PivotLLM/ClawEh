package gateway

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/channels/device"
)

// TestAutoApproveLocalDevice verifies the bridge self-pairs its own pending
// request in the store and leaves the device paired.
func TestAutoApproveLocalDevice(t *testing.T) {
	dataDir := t.TempDir()
	stateDir := filepath.Join(dataDir, "state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	store, err := device.OpenStore(filepath.Join(stateDir, "gateway.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	ctx := context.Background()

	const deviceID = "dev-abc"
	if _, err := store.CreatePending(ctx, device.PendingPairing{
		DeviceID:  deviceID,
		PublicKey: "pubkey-abc",
		Role:      "node",
	}); err != nil {
		t.Fatalf("CreatePending: %v", err)
	}
	// Also enqueue an unrelated device to prove we only approve our own.
	if _, err := store.CreatePending(ctx, device.PendingPairing{
		DeviceID:  "other-device",
		PublicKey: "pubkey-other",
		Role:      "node",
	}); err != nil {
		t.Fatalf("CreatePending other: %v", err)
	}
	_ = store.Close() // the helper opens its own handle

	if _, err := autoApproveLocalDevice(ctx, dataDir, deviceID); err != nil {
		t.Fatalf("autoApproveLocalDevice: %v", err)
	}

	verify, err := device.OpenStore(filepath.Join(stateDir, "gateway.db"))
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer func() { _ = verify.Close() }()
	if _, ok, err := verify.GetPaired(ctx, deviceID); err != nil || !ok {
		t.Fatalf("device %s not paired after auto-approve (ok=%v err=%v)", deviceID, ok, err)
	}
	if _, ok, _ := verify.GetPaired(ctx, "other-device"); ok {
		t.Fatalf("unrelated device was wrongly approved")
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
	store, err := device.OpenStore(filepath.Join(stateDir, "gateway.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	_ = store.Close()

	if _, err := autoApproveLocalDevice(context.Background(), dataDir, "missing"); err == nil {
		t.Fatalf("expected error when no pending pairing exists")
	}
}
