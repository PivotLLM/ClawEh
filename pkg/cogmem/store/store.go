// ClawEh - Cognitive Memory
// License: MIT

package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	_ "modernc.org/sqlite"
)

// DBTX is the subset of *sql.DB / *sql.Tx used by store methods, so the same
// code path runs standalone or inside a transaction (e.g. the consolidation
// worker applies all ops in one tx).
type DBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Store owns one .cogmem.db.
type Store struct {
	db   *sql.DB
	path string
}

// Option configures Open (functional options pattern, per dev standards).
type Option func(*openConfig)

type openConfig struct {
	busyTimeout time.Duration
}

// WithBusyTimeout overrides the SQLite busy_timeout (default defaultBusyTimeout).
func WithBusyTimeout(d time.Duration) Option {
	return func(c *openConfig) {
		if d > 0 {
			c.busyTimeout = d
		}
	}
}

// Open opens (or creates) a cogmem database at path with WAL mode and runs
// migrations. Pure Go (modernc.org/sqlite), no CGO.
func Open(path string, opts ...Option) (*Store, error) {
	cfg := openConfig{busyTimeout: defaultBusyTimeout}
	for _, o := range opts {
		o(&cfg)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("cogmem: open %s: %w", path, err)
	}
	pragmas := []string{
		"PRAGMA journal_mode=" + journalMode,
		fmt.Sprintf("PRAGMA busy_timeout=%d", cfg.busyTimeout.Milliseconds()),
		"PRAGMA foreign_keys=ON",
		"PRAGMA synchronous=" + synchronousMode,
	}
	for _, p := range pragmas {
		if _, err := db.ExecContext(context.Background(), p); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("cogmem: %q: %w", p, err)
		}
	}
	s := &Store{db: db, path: path}
	if err := s.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// DB exposes the underlying handle for callers needing direct access (e.g. the
// composer's read path). Treat as read-only outside store methods.
func (s *Store) DB() *sql.DB { return s.db }

// Path returns the database file path.
func (s *Store) Path() string { return s.path }

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("cogmem: schema: %w", err)
	}
	// Seed bookkeeping rows if absent. IDs are random (constants.go), so no
	// counters are needed — only the stable-block generation.
	seeds := map[string]string{"stable_rev": "0"}
	for k, v := range seeds {
		if _, err := s.db.ExecContext(ctx,
			`INSERT OR IGNORE INTO meta(key,value) VALUES(?,?)`, k, v); err != nil {
			return fmt.Errorf("cogmem: seed meta %q: %w", k, err)
		}
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES(?,?)`,
		schemaVersion, now()); err != nil {
		return fmt.Errorf("cogmem: record migration: %w", err)
	}
	return nil
}

// WithTx runs fn inside a transaction, committing on success and rolling back
// on error or panic.
func (s *Store) WithTx(ctx context.Context, fn func(tx *sql.Tx) error) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// --- meta / counters / stable_rev ---

func getMetaInt(ctx context.Context, q DBTX, key string) (int64, error) {
	var v string
	err := q.QueryRowContext(ctx, `SELECT value FROM meta WHERE key=?`, key).Scan(&v)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(v, 10, 64)
}

func setMetaInt(ctx context.Context, q DBTX, key string, val int64) error {
	_, err := q.ExecContext(ctx,
		`INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
		key, strconv.FormatInt(val, 10))
	return err
}

// StableRev returns the current stable-block generation. The composer caches its
// stable block and rebuilds only when this changes.
func (s *Store) StableRev(ctx context.Context) (int64, error) {
	return getMetaInt(ctx, s.db, "stable_rev")
}

// bumpStableRev increments the generation counter; call within the same tx as
// any change to always-on content (always-on domains, the index, or pending).
func bumpStableRev(ctx context.Context, q DBTX) error {
	_, err := q.ExecContext(ctx,
		`UPDATE meta SET value = CAST(CAST(value AS INTEGER) + 1 AS TEXT) WHERE key='stable_rev'`)
	return err
}

// genID returns a candidate id: prefix + idRandomLen Crockford base32 chars.
func genID(prefix string) (string, error) {
	buf := make([]byte, idRandomLen)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	out := make([]byte, idRandomLen)
	for i, b := range buf {
		out[i] = crockfordAlphabet[int(b)%32] // 256%32==0 → unbiased
	}
	return prefix + string(out), nil
}

// freshID generates a unique id for the given table (an internal constant, never
// user input), retrying on the rare collision.
func freshID(ctx context.Context, q DBTX, prefix, table string) (string, error) {
	for i := 0; i < idMaxAttempts; i++ {
		id, err := genID(prefix)
		if err != nil {
			return "", err
		}
		var x int
		err = q.QueryRowContext(ctx, "SELECT 1 FROM "+table+" WHERE id=?", id).Scan(&x)
		if err == sql.ErrNoRows {
			return id, nil
		}
		if err != nil {
			return "", err
		}
	}
	return "", errors.New("cogmem: id allocation failed after retries")
}

// now returns the current unix time in seconds. Centralized so tests can reason
// about timestamps; cogmem rarely needs sub-second precision.
func now() int64 { return time.Now().Unix() }

// nowNano returns unix nanoseconds, used only as a strictly-increasing ordering
// key for last_active_at (so two touches in the same second still order).
func nowNano() int64 { return time.Now().UnixNano() }

func derefOr0(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func unixPtr(t *int64) *time.Time {
	if t == nil {
		return nil
	}
	tt := time.Unix(*t, 0)
	return &tt
}

func timeUnix(t int64) time.Time { return time.Unix(t, 0) }

// placeholders returns "?,?,..." with n entries for an IN clause.
func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	b := make([]byte, 0, 2*n)
	for i := 0; i < n; i++ {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, '?')
	}
	return string(b)
}
