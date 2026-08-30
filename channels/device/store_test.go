package device

import (
	"context"
	"path/filepath"
	"testing"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestPairingLifecycle(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	// Unknown device is not paired.
	if _, ok, err := s.GetPaired(ctx, "dev-1"); err != nil || ok {
		t.Fatalf("GetPaired unknown: ok=%v err=%v", ok, err)
	}

	// Create a pending request.
	reqID, err := s.CreatePending(ctx, PendingPairing{
		DeviceID: "dev-1", PublicKey: "pk", DisplayName: "Rabbit R1",
		ClientID: "rabbit-r1", ClientMode: "node", Role: "node",
	})
	if err != nil {
		t.Fatalf("CreatePending: %v", err)
	}
	pend, err := s.ListPending(ctx)
	if err != nil || len(pend) != 1 || pend[0].RequestID != reqID || pend[0].DisplayName != "Rabbit R1" {
		t.Fatalf("ListPending: %+v err=%v", pend, err)
	}

	// A device's reconnect re-creates its pending; the request id must stay STABLE
	// (one pending per device) so an operator's approve doesn't race the loop.
	reqID2, err := s.CreatePending(ctx, PendingPairing{DeviceID: "dev-1", PublicKey: "pk"})
	if err != nil {
		t.Fatalf("CreatePending(2): %v", err)
	}
	if reqID2 != reqID {
		t.Fatalf("request id churned across re-create: %s -> %s", reqID, reqID2)
	}
	if pend, _ := s.ListPending(ctx); len(pend) != 1 {
		t.Fatalf("expected 1 pending after replace, got %d", len(pend))
	}

	// Reject removes it.
	cur, _ := s.ListPending(ctx)
	if err := s.Reject(ctx, cur[0].RequestID); err != nil {
		t.Fatalf("Reject: %v", err)
	}
	if pend, _ := s.ListPending(ctx); len(pend) != 0 {
		t.Fatalf("expected 0 pending after reject")
	}
	if err := s.Reject(ctx, "nonexistent"); err != ErrPendingNotFound {
		t.Fatalf("Reject unknown: want ErrPendingNotFound got %v", err)
	}
}

func TestApproveMintsTokensAndPairs(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	reqID, err := s.CreatePending(ctx, PendingPairing{
		DeviceID: "dev-2", PublicKey: "pk2", DisplayName: "Rabbit R1", Role: "node",
	})
	if err != nil {
		t.Fatal(err)
	}

	dev, tokens, err := s.Approve(ctx, reqID, []string{"node", "operator"}, []string{"operator.write"})
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if dev.DeviceID != "dev-2" || len(dev.Roles) != 2 {
		t.Fatalf("paired device: %+v", dev)
	}
	if len(tokens) != 2 {
		t.Fatalf("expected 2 tokens (one per role), got %d", len(tokens))
	}

	// Pending is consumed; device is now paired.
	if pend, _ := s.ListPending(ctx); len(pend) != 0 {
		t.Fatalf("pending should be consumed after approve")
	}
	got, ok, err := s.GetPaired(ctx, "dev-2")
	if err != nil || !ok || got.PublicKey != "pk2" {
		t.Fatalf("GetPaired after approve: ok=%v err=%v dev=%+v", ok, err, got)
	}

	// Each token validates and resolves to the device + role.
	for _, tok := range tokens {
		dt, ok, err := s.TokenByValue(ctx, tok.Token)
		if err != nil || !ok || dt.DeviceID != "dev-2" || dt.Role != tok.Role {
			t.Fatalf("TokenByValue(%s): ok=%v err=%v dt=%+v", tok.Role, ok, err, dt)
		}
	}

	// Removing the device revokes its tokens (lookup fails).
	if err := s.RemovePaired(ctx, "dev-2"); err != nil {
		t.Fatalf("RemovePaired: %v", err)
	}
	if _, ok, _ := s.GetPaired(ctx, "dev-2"); ok {
		t.Fatalf("device should be gone after RemovePaired")
	}
	if _, ok, _ := s.TokenByValue(ctx, tokens[0].Token); ok {
		t.Fatalf("token should be gone after RemovePaired")
	}
}

func TestApproveUnknownRequest(t *testing.T) {
	s := openTestStore(t)
	if _, _, err := s.Approve(context.Background(), "nope", nil, nil); err != ErrPendingNotFound {
		t.Fatalf("want ErrPendingNotFound, got %v", err)
	}
}
