package device

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
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
	if err := s.Reject(ctx, "nonexistent"); !errors.Is(err, ErrPendingNotFound) {
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
	if _, _, err := s.Approve(context.Background(), "nope", nil, nil); !errors.Is(err, ErrPendingNotFound) {
		t.Fatalf("want ErrPendingNotFound, got %v", err)
	}
}

// TestOpenStoreUnderContention is the regression guard for an intermittent 500
// on the WebUI devices page: `store open failed`, roughly one gateway restart in
// three. The captured error was:
//
//	device: "PRAGMA journal_mode=WAL": database is locked (SQLITE_BUSY)
//
// The cause was pragma ORDER. CONVERTING a database to WAL takes an exclusive
// lock, and journal_mode was set before busy_timeout — so while another
// connection held a write lock on the same file, the open failed INSTANTLY
// instead of waiting. busy_timeout needs no lock, so it must come first, and it
// then covers everything after it.
//
// Reproducing it needs a database that is NOT already in WAL mode, which is the
// state a fresh install is in on the very first open — precisely the startup
// race between the device channel and this API. Against an already-WAL file the
// pragma is a no-op and nothing contends, which is why this only ever bit on
// some restarts.
func TestOpenStoreUnderContention(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway.db")

	// A rollback-journal database, i.e. what OpenStore meets before anything has
	// converted it. Opened directly so this stays non-WAL.
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	defer func() { _ = raw.Close() }()
	ctx := context.Background()
	for _, p := range []string{"PRAGMA journal_mode=DELETE", "PRAGMA busy_timeout=5000"} {
		if _, err := raw.ExecContext(ctx, p); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
	}
	if _, err := raw.ExecContext(ctx, `CREATE TABLE seed (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("seed table: %v", err)
	}

	// Hold a write lock, so the racing open must wait for it.
	tx, err := raw.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO seed (id) VALUES (1)`); err != nil {
		t.Fatalf("write inside tx: %v", err)
	}

	released := make(chan struct{})
	go func() {
		time.Sleep(150 * time.Millisecond)
		_ = tx.Commit()
		close(released)
	}()

	start := time.Now()
	racer, err := OpenStore(path)
	elapsed := time.Since(start)
	<-released
	if err != nil {
		t.Fatalf("OpenStore lost to a held write lock after %v: %v\n"+
			"busy_timeout must be set BEFORE journal_mode, or converting to WAL fails instantly",
			elapsed, err)
	}
	_ = racer.Close()
}

// TestOpenStoreSetsBusyTimeoutFirst pins the ordering directly, so the reason
// survives even if the contention test above is ever weakened or made lenient.
func TestOpenStoreSetsBusyTimeoutFirst(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	var timeout int
	if err := store.db.QueryRow("PRAGMA busy_timeout").Scan(&timeout); err != nil {
		t.Fatalf("read busy_timeout: %v", err)
	}
	if timeout <= 0 {
		t.Fatalf("busy_timeout = %d, want a positive value: without it a contended open fails instantly", timeout)
	}

	var mode string
	if err := store.db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", mode)
	}
}
