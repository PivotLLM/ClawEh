// ClawEh
// License: MIT

package fusion

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestDataStore_PragmasReachEveryPooledConnection pins that the connection
// settings travel in the DSN. Exec'ing them after sql.Open configured only the
// one connection that ran them; database/sql opens more under load, and those
// ran with busy_timeout=0. Pinning the first connection in a transaction forces
// the next statement onto a fresh second one.
func TestDataStore_PragmasReachEveryPooledConnection(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	t.Cleanup(func() {
		if rbErr := tx.Rollback(); rbErr != nil {
			t.Errorf("rollback: %v", rbErr)
		}
	})

	var timeout, fk, sync int
	var journal string
	if err := s.db.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&timeout); err != nil {
		t.Fatalf("busy_timeout: %v", err)
	}
	if err := s.db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatalf("foreign_keys: %v", err)
	}
	if err := s.db.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&sync); err != nil {
		t.Fatalf("synchronous: %v", err)
	}
	if err := s.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journal); err != nil {
		t.Fatalf("journal_mode: %v", err)
	}
	if timeout != 5000 || fk != 1 || sync != 1 || journal != "wal" {
		t.Fatalf("second connection: busy_timeout=%d foreign_keys=%d synchronous=%d journal_mode=%q, want 5000/1/1/wal",
			timeout, fk, sync, journal)
	}
}

// TestDataStore_CreatesPrivateFiles pins that a fresh token store and its WAL
// side file are owner-only from the first write, not from the next startup's
// permission sweep. SQLite alone would create them 0644.
func TestDataStore_CreatesPrivateFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits")
	}
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state", "fusion-tokens.db")
	ds, err := NewSQLiteDataStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteDataStore: %v", err)
	}
	s, ok := ds.(*sqliteDataStore)
	if !ok {
		t.Fatalf("NewSQLiteDataStore returned %T, want *sqliteDataStore", ds)
	}
	defer func() {
		if err := s.db.Close(); err != nil {
			t.Errorf("close db: %v", err)
		}
	}()
	if err := s.Set(ctx, "oauth", "k", []byte("v")); err != nil {
		t.Fatalf("Set: %v", err)
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

// TestDataStore_ContendedWriteWaits: a Set on a second connection while the
// first holds the write lock must wait for it, not fail at once with
// "database is locked".
func TestDataStore_ContendedWriteWaits(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, insErr := tx.ExecContext(ctx,
		`INSERT INTO kv (collection, key, value) VALUES ('oauth', 'held', X'00')`); insErr != nil {
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
	err = s.Set(ctx, "oauth", "other", []byte("v"))
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("contended Set failed after %v: %v (busy_timeout is not set on the second connection)", elapsed, err)
	}
	if elapsed < hold/2 {
		t.Fatalf("contended Set returned after %v without waiting for the %v lock", elapsed, hold)
	}
}

func newTestStore(t *testing.T) *sqliteDataStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state", "fusion-tokens.db")
	ds, err := NewSQLiteDataStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteDataStore: %v", err)
	}
	s, ok := ds.(*sqliteDataStore)
	if !ok {
		t.Fatalf("NewSQLiteDataStore returned %T, want *sqliteDataStore", ds)
	}
	t.Cleanup(func() {
		if err := s.db.Close(); err != nil {
			t.Errorf("close db: %v", err)
		}
	})
	return s
}

func TestDataStore_RoundTrip(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	want := []byte(`{"token":"abc"}`)
	if err := s.Set(ctx, "oauth", "alice/graph", want); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, ok, err := s.Get(ctx, "oauth", "alice/graph")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("Get: ok=false for a value that was just Set")
	}
	if !bytes.Equal(got, want) {
		t.Errorf("Get value = %q, want %q", got, want)
	}

	// Set overwrites the prior value under the same (collection,key).
	next := []byte(`{"token":"xyz"}`)
	if setErr := s.Set(ctx, "oauth", "alice/graph", next); setErr != nil {
		t.Fatalf("Set overwrite: %v", setErr)
	}
	got, _, err = s.Get(ctx, "oauth", "alice/graph")
	if err != nil {
		t.Fatalf("Get after overwrite: %v", err)
	}
	if !bytes.Equal(got, next) {
		t.Errorf("after overwrite value = %q, want %q", got, next)
	}
}

func TestDataStore_CollectionIsolation(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if err := s.Set(ctx, "oauth", "k", []byte("oauth-val")); err != nil {
		t.Fatalf("Set oauth: %v", err)
	}
	if err := s.Set(ctx, "creds", "k", []byte("creds-val")); err != nil {
		t.Fatalf("Set creds: %v", err)
	}

	got, ok, err := s.Get(ctx, "oauth", "k")
	if err != nil || !ok {
		t.Fatalf("Get oauth: ok=%v err=%v", ok, err)
	}
	if string(got) != "oauth-val" {
		t.Errorf("oauth/k = %q, want oauth-val (collection bleed)", got)
	}
	got, ok, err = s.Get(ctx, "creds", "k")
	if err != nil || !ok {
		t.Fatalf("Get creds: ok=%v err=%v", ok, err)
	}
	if string(got) != "creds-val" {
		t.Errorf("creds/k = %q, want creds-val (collection bleed)", got)
	}
}

func TestDataStore_AbsentKey(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	got, ok, err := s.Get(ctx, "oauth", "missing")
	if err != nil {
		t.Fatalf("Get absent: unexpected error %v", err)
	}
	if ok {
		t.Error("Get absent: ok=true, want false")
	}
	if got != nil {
		t.Errorf("Get absent: value=%q, want nil", got)
	}
}

func TestDataStore_DeleteIdempotent(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if err := s.Set(ctx, "authcodes", "code1", []byte("v")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := s.Delete(ctx, "authcodes", "code1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok, err := s.Get(ctx, "authcodes", "code1"); err != nil {
		t.Fatalf("Get after Delete: %v", err)
	} else if ok {
		t.Error("Get after Delete: ok=true, want false")
	}
	// Deleting an absent record is not an error.
	if err := s.Delete(ctx, "authcodes", "code1"); err != nil {
		t.Errorf("Delete absent: unexpected error %v", err)
	}
}
