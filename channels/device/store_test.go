package device

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/internal/tokenhash"
)

// rawTokens reads the device_tokens.token column as stored.
func rawTokens(t *testing.T, s *Store) []string {
	t.Helper()
	rows, err := s.db.QueryContext(context.Background(), `SELECT token FROM device_tokens ORDER BY created_at_ms, role`)
	if err != nil {
		t.Fatalf("query tokens: %v", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Errorf("rows close: %v", err)
		}
	}()
	var out []string
	for rows.Next() {
		var tok string
		if err := rows.Scan(&tok); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, tok)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}

// TestTokensStoredHashed: the database never holds a device token, only its
// hash, and the plaintext handed out at approval still authenticates.
func TestTokensStoredHashed(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	reqID, err := s.CreatePending(ctx, PendingPairing{DeviceID: "dev-h", PublicKey: "pk", Role: "node"})
	if err != nil {
		t.Fatalf("CreatePending: %v", err)
	}
	_, tokens, err := s.Approve(ctx, reqID, []string{"node", "operator"}, nil)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	stored := rawTokens(t, s)
	if len(stored) != len(tokens) {
		t.Fatalf("stored %d tokens, minted %d", len(stored), len(tokens))
	}
	for _, tok := range tokens {
		want := tokenhash.Hash(tok.Token)
		found := false
		for _, raw := range stored {
			if raw == tok.Token {
				t.Errorf("plaintext token for role %s is in the database", tok.Role)
			}
			if raw == want {
				found = true
			}
		}
		if !found {
			t.Errorf("hash of the %s token is not in the database", tok.Role)
		}
		dt, ok, err := s.TokenByValue(ctx, tok.Token)
		if err != nil || !ok || dt.Role != tok.Role || dt.Token != tok.Token {
			t.Errorf("TokenByValue(%s) = %+v ok=%v err=%v", tok.Role, dt, ok, err)
		}
	}
	// The hash itself is not a credential.
	if _, ok, err := s.TokenByValue(ctx, stored[0]); err != nil || ok {
		t.Errorf("presenting a stored hash authenticated (ok=%v err=%v)", ok, err)
	}
}

// TestOpenStore_HashesPlaintextTokensOnce covers a database written before
// tokens were hashed: reopening it hashes every plaintext row in place, the
// device's own copy keeps authenticating, and a further reopen changes nothing.
func TestOpenStore_HashesPlaintextTokensOnce(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "gateway.db")
	s, err := OpenStore(ctx, path)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	const plain = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if _, err = s.db.ExecContext(ctx, `INSERT INTO device_tokens (token, device_id, role, scopes, created_at_ms) VALUES (?,?,?,?,?)`,
		plain, "dev-old", "node", "[]", 1); err != nil {
		t.Fatalf("seed plaintext row: %v", err)
	}
	if err = s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	for pass := 1; pass <= 2; pass++ {
		s, err = OpenStore(ctx, path)
		if err != nil {
			t.Fatalf("OpenStore pass %d: %v", pass, err)
		}
		if got := rawTokens(t, s); len(got) != 1 || got[0] != tokenhash.Hash(plain) {
			t.Fatalf("pass %d: stored tokens = %v, want [%s]", pass, got, tokenhash.Hash(plain))
		}
		dt, ok, err := s.TokenByValue(ctx, plain)
		if err != nil || !ok || dt.DeviceID != "dev-old" {
			t.Fatalf("pass %d: TokenByValue(plain) = %+v ok=%v err=%v", pass, dt, ok, err)
		}
		if err := s.Close(); err != nil {
			t.Fatalf("Close pass %d: %v", pass, err)
		}
	}
}

// TestIssueTokens_Rotates: reissuing replaces the device's tokens, one per
// role; the old ones stop authenticating and the new ones work.
func TestIssueTokens_Rotates(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	reqID, err := s.CreatePending(ctx, PendingPairing{DeviceID: "dev-r", PublicKey: "pk", Role: "node"})
	if err != nil {
		t.Fatalf("CreatePending: %v", err)
	}
	_, old, err := s.Approve(ctx, reqID, []string{"node", "operator"}, []string{"operator.write"})
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}

	fresh, err := s.IssueTokens(ctx, "dev-r", []string{"node", "operator"}, []string{"operator.write"})
	if err != nil {
		t.Fatalf("IssueTokens: %v", err)
	}
	if len(fresh) != 2 || fresh[0].Role != "node" || fresh[1].Role != "operator" {
		t.Fatalf("IssueTokens = %+v, want one token per role in order", fresh)
	}
	if got := rawTokens(t, s); len(got) != 2 {
		t.Fatalf("expected exactly 2 stored tokens after rotation, got %d", len(got))
	}
	for _, o := range old {
		if _, ok, lookupErr := s.TokenByValue(ctx, o.Token); lookupErr != nil || ok {
			t.Errorf("old %s token still authenticates (ok=%v err=%v)", o.Role, ok, lookupErr)
		}
	}
	for _, f := range fresh {
		dt, ok, lookupErr := s.TokenByValue(ctx, f.Token)
		if lookupErr != nil || !ok || dt.DeviceID != "dev-r" || dt.Role != f.Role || len(dt.Scopes) != 1 {
			t.Errorf("fresh %s token: %+v ok=%v err=%v", f.Role, dt, ok, lookupErr)
		}
	}

	// No roles falls back to a single node token.
	only, err := s.IssueTokens(ctx, "dev-r", nil, nil)
	if err != nil || len(only) != 1 || only[0].Role != "node" {
		t.Fatalf("IssueTokens(no roles) = %+v err=%v, want one node token", only, err)
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(context.Background(), filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
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
	if pend, listErr := s.ListPending(ctx); listErr != nil || len(pend) != 1 {
		t.Fatalf("expected 1 pending after replace, got %d (err %v)", len(pend), listErr)
	}

	// Reject removes it.
	cur, err := s.ListPending(ctx)
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	if err := s.Reject(ctx, cur[0].RequestID); err != nil {
		t.Fatalf("Reject: %v", err)
	}
	if pend, listErr := s.ListPending(ctx); listErr != nil || len(pend) != 0 {
		t.Fatalf("expected 0 pending after reject, got %d (err %v)", len(pend), listErr)
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
	if pend, listErr := s.ListPending(ctx); listErr != nil || len(pend) != 0 {
		t.Fatalf("pending should be consumed after approve, got %d (err %v)", len(pend), listErr)
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
	if _, ok, getErr := s.GetPaired(ctx, "dev-2"); getErr != nil || ok {
		t.Fatalf("device should be gone after RemovePaired (ok=%v err=%v)", ok, getErr)
	}
	if _, ok, tokErr := s.TokenByValue(ctx, tokens[0].Token); tokErr != nil || ok {
		t.Fatalf("token should be gone after RemovePaired (ok=%v err=%v)", ok, tokErr)
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
	defer func() {
		if closeErr := raw.Close(); closeErr != nil {
			t.Errorf("Close: %v", closeErr)
		}
	}()
	ctx := context.Background()
	for _, p := range []string{"PRAGMA journal_mode=DELETE", "PRAGMA busy_timeout=5000"} {
		if _, pragmaErr := raw.ExecContext(ctx, p); pragmaErr != nil {
			t.Fatalf("%s: %v", p, pragmaErr)
		}
	}
	if _, seedErr := raw.ExecContext(ctx, `CREATE TABLE seed (id INTEGER PRIMARY KEY)`); seedErr != nil {
		t.Fatalf("seed table: %v", seedErr)
	}

	// Hold a write lock, so the racing open must wait for it.
	tx, err := raw.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, insErr := tx.ExecContext(ctx, `INSERT INTO seed (id) VALUES (1)`); insErr != nil {
		t.Fatalf("write inside tx: %v", insErr)
	}

	released := make(chan struct{})
	go func() {
		time.Sleep(150 * time.Millisecond)
		if commitErr := tx.Commit(); commitErr != nil {
			t.Errorf("commit: %v", commitErr)
		}
		close(released)
	}()

	start := time.Now()
	racer, err := OpenStore(context.Background(), path)
	elapsed := time.Since(start)
	<-released
	if err != nil {
		t.Fatalf("OpenStore lost to a held write lock after %v: %v\n"+
			"busy_timeout must be set BEFORE journal_mode, or converting to WAL fails instantly",
			elapsed, err)
	}
	if err := racer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestOpenStoreSetsBusyTimeoutFirst pins the ordering directly, so the reason
// survives even if the contention test above is ever weakened or made lenient.
func TestOpenStoreSetsBusyTimeoutFirst(t *testing.T) {
	store, err := OpenStore(context.Background(), filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()

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

// TestOpenStorePragmasReachEveryPooledConnection pins that the connection
// settings travel in the DSN. Exec'ing them after sql.Open configured only the
// one connection that happened to run them; database/sql opens more under
// load, and those ran with busy_timeout=0 and foreign_keys off. The test pins
// the first connection inside a transaction so the next statement has to come
// from a fresh second connection, then reads the settings there.
func TestOpenStorePragmasReachEveryPooledConnection(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer rollback(tx)

	var timeout, fk, sync int
	if err := s.db.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&timeout); err != nil {
		t.Fatalf("busy_timeout: %v", err)
	}
	if err := s.db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatalf("foreign_keys: %v", err)
	}
	if err := s.db.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&sync); err != nil {
		t.Fatalf("synchronous: %v", err)
	}
	if timeout != 5000 || fk != 1 || sync != 1 {
		t.Fatalf("second connection: busy_timeout=%d foreign_keys=%d synchronous=%d, want 5000/1/1 (NORMAL)",
			timeout, fk, sync)
	}
}

// TestOpenStoreCreatesPrivateFiles pins that a fresh pairing database and its
// WAL side file are owner-only from the first write, not from the next
// startup's permission sweep. SQLite alone would create them 0644.
func TestOpenStoreCreatesPrivateFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits")
	}
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "gateway.db")
	s, err := OpenStore(ctx, path)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()
	if _, err := s.CreatePending(ctx, PendingPairing{DeviceID: "d1", PublicKey: "pk", Role: "node"}); err != nil {
		t.Fatalf("CreatePending: %v", err)
	}
	for _, p := range []string{path, path + "-wal"} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		if got := fi.Mode().Perm(); got != 0o600 {
			t.Errorf("%s mode = %04o, want 0600", filepath.Base(p), got)
		}
	}
}

// TestStoreContendedWriteWaits is the behavioural form of the test above: a
// write on a second connection while the first holds the write lock must wait
// for it, not fail at once with "database is locked".
func TestStoreContendedWriteWaits(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE probe (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("probe table: %v", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, insErr := tx.ExecContext(ctx, `INSERT INTO probe (id) VALUES (1)`); insErr != nil {
		t.Fatalf("write inside tx: %v", insErr)
	}
	const hold = 150 * time.Millisecond
	go func() {
		time.Sleep(hold)
		if commitErr := tx.Commit(); commitErr != nil {
			t.Errorf("commit: %v", commitErr)
		}
	}()

	start := time.Now()
	_, err = s.db.ExecContext(ctx, `INSERT INTO probe (id) VALUES (2)`)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("contended write failed after %v: %v (busy_timeout is not set on the second connection)", elapsed, err)
	}
	if elapsed < hold/2 {
		t.Fatalf("contended write returned after %v without waiting for the %v lock", elapsed, hold)
	}
}
