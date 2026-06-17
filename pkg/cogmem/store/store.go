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
	"strings"
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
	// Legacy rename (hook→memory, kind→type) must run BEFORE the schema DDL, or
	// CREATE TABLE IF NOT EXISTS memories would make a fresh empty table beside
	// the existing data. Idempotent and a no-op on fresh databases.
	if err := s.renameLegacyTables(ctx); err != nil {
		return fmt.Errorf("cogmem: legacy rename: %w", err)
	}
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
	if err := s.ensureDomainColumns(ctx); err != nil {
		return fmt.Errorf("cogmem: ensure domain columns: %w", err)
	}
	if err := s.ensureTypeValues(ctx); err != nil {
		return fmt.Errorf("cogmem: normalize type values: %w", err)
	}
	// Merge any duplicate general domains (from a pre-fix concurrent-open race)
	// into one BEFORE creating the unique index that prevents recurrence.
	if err := s.dedupeGeneralDomains(ctx); err != nil {
		return fmt.Errorf("cogmem: dedupe general: %w", err)
	}
	if _, err := s.db.ExecContext(ctx,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_one_general ON domains(type) WHERE type='general'`); err != nil {
		return fmt.Errorf("cogmem: general unique index: %w", err)
	}
	if err := s.ensureGeneralDomain(ctx); err != nil {
		return fmt.Errorf("cogmem: seed general domain: %w", err)
	}
	return nil
}

// DedupeActiveMemories retires active memories that exactly duplicate an earlier
// active memory in the same domain (same domain_id + text). The earliest by
// created_at (then id) is kept; the rest are retired with reason "duplicate".
// Returns how many were retired. Idempotent: a no-op once there are no dups.
func (s *Store) DedupeActiveMemories(ctx context.Context) (int, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.id FROM memories m
		WHERE m.status = ?
		  AND EXISTS (
		    SELECT 1 FROM memories e
		    WHERE e.domain_id = m.domain_id AND e.text = m.text AND e.status = ?
		      AND (e.created_at < m.created_at OR (e.created_at = m.created_at AND e.id < m.id))
		  )`, string(StatusActive), string(StatusActive))
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}
	args := make([]any, 0, len(ids)+2)
	args = append(args, string(StatusRetired), now())
	for _, id := range ids {
		args = append(args, id)
	}
	err = s.WithTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`UPDATE memories SET status=?, retire_reason='duplicate', updated_at=? WHERE id IN (`+
				placeholders(len(ids))+`)`, args...); err != nil {
			return err
		}
		return bumpStableRev(ctx, tx)
	})
	if err != nil {
		return 0, err
	}
	return len(ids), nil
}

// PurgeStats reports how many rows a purge removed (or would remove on a dry run).
type PurgeStats struct {
	Memories int64
	Domains  int64
}

// PurgeNonActive hard-deletes everything that is not current active memory: every
// domain whose status is not active (and all of its memories, regardless of their
// status), and every non-active memory in the surviving active domains. Only
// active memories in active domains remain. When apply is false it counts what
// would be removed without deleting. Returns the counts either way.
func (s *Store) PurgeNonActive(ctx context.Context, apply bool) (PurgeStats, error) {
	var st PurgeStats
	// Memories removed: non-active, or belonging to a non-active domain.
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM memories
		WHERE status != ? OR domain_id IN (SELECT id FROM domains WHERE status != ?)`,
		string(StatusActive), string(StatusActive)).Scan(&st.Memories); err != nil {
		return st, err
	}
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM domains WHERE status != ?`, string(StatusActive)).Scan(&st.Domains); err != nil {
		return st, err
	}
	if !apply || (st.Memories == 0 && st.Domains == 0) {
		return st, nil
	}
	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		// Delete memories first (FK: memories reference domains). This clears every
		// non-active memory plus all memories under soon-to-be-deleted domains.
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM memories
			WHERE status != ? OR domain_id IN (SELECT id FROM domains WHERE status != ?)`,
			string(StatusActive), string(StatusActive)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM domains WHERE status != ?`, string(StatusActive)); err != nil {
			return err
		}
		// Pending digest / always-on content may have changed; force a rebuild.
		return bumpStableRev(ctx, tx)
	})
	return st, err
}

// Vacuum reclaims disk space after deletions. Must run outside a transaction.
func (s *Store) Vacuum(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `VACUUM`)
	return err
}

// dedupeGeneralDomains collapses multiple "general" domains into one. A pre-fix
// race (concurrent store opens both seeding general before the unique index
// existed) could create duplicates. The oldest is canonical; every other
// general's memories are repointed onto it and the empty extra is deleted.
func (s *Store) dedupeGeneralDomains(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id FROM domains WHERE type=? ORDER BY created_at, id`, string(DomainGeneral))
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(ids) <= 1 {
		return nil
	}
	canonical, extras := ids[0], ids[1:]
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		for _, ex := range extras {
			if _, err := tx.ExecContext(ctx,
				`UPDATE memories SET domain_id=? WHERE domain_id=?`, canonical, ex); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM domains WHERE id=?`, ex); err != nil {
				return err
			}
		}
		// Merged content changed the always-on block; force a cache rebuild.
		return bumpStableRev(ctx, tx)
	})
}

// ensureTypeValues folds retired type values into the current set so stored data
// matches the slimmed model: memory types project_state/workflow/lesson → fact,
// and the dropped domain type repo → project. Idempotent (no-op once normalized).
func (s *Store) ensureTypeValues(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE memories SET type='fact' WHERE type IN ('project_state','workflow','lesson')`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE domains SET type='project' WHERE type='repo'`); err != nil {
		return err
	}
	return nil
}

// renameLegacyTables migrates a pre-rename database (hooks→memories, kind→type,
// supersedes_hook_id→supersedes_memory_id, memory_events.hook_id→memory_id) in
// place. Every step is guarded so it is idempotent and a no-op on fresh DBs.
func (s *Store) renameLegacyTables(ctx context.Context) error {
	hooksExists, err := s.tableExists(ctx, "hooks")
	if err != nil {
		return err
	}
	memExists, err := s.tableExists(ctx, "memories")
	if err != nil {
		return err
	}
	if hooksExists && !memExists {
		if _, err := s.db.ExecContext(ctx, `ALTER TABLE hooks RENAME TO memories`); err != nil {
			return err
		}
		// Indexes follow the renamed table but keep their idx_hooks_* names; drop
		// them so the schema DDL can recreate idx_memories_* without duplication.
		for _, idx := range []string{"idx_hooks_domain", "idx_hooks_status"} {
			if _, err := s.db.ExecContext(ctx, `DROP INDEX IF EXISTS `+idx); err != nil {
				return err
			}
		}
	}
	renames := []struct{ table, old, new string }{
		{"memories", "kind", "type"},
		{"memories", "supersedes_hook_id", "supersedes_memory_id"},
		{"memory_events", "hook_id", "memory_id"},
	}
	for _, r := range renames {
		cols, err := s.columnSet(ctx, r.table)
		if err != nil {
			return err
		}
		if cols[r.old] && !cols[r.new] {
			if _, err := s.db.ExecContext(ctx,
				fmt.Sprintf(`ALTER TABLE %s RENAME COLUMN %s TO %s`, r.table, r.old, r.new)); err != nil {
				return err
			}
		}
	}
	return nil
}

// tableExists reports whether a table of the given name exists.
func (s *Store) tableExists(ctx context.Context, name string) (bool, error) {
	var x string
	err := s.db.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&x)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// ensureDomainColumns adds columns introduced after a database was first created.
// CREATE TABLE IF NOT EXISTS does not add columns to an existing table, so each
// additive column gets an idempotent ALTER guarded by a table_info check.
func (s *Store) ensureDomainColumns(ctx context.Context) error {
	have, err := s.columnSet(ctx, "domains")
	if err != nil {
		return err
	}
	if !have["triggers"] {
		if _, err := s.db.ExecContext(ctx,
			`ALTER TABLE domains ADD COLUMN triggers TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	if !have["keyword_triggers"] {
		if _, err := s.db.ExecContext(ctx,
			`ALTER TABLE domains ADD COLUMN keyword_triggers TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	return nil
}

// columnSet returns the set of column names on a table.
func (s *Store) columnSet(ctx context.Context, table string) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	set := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		set[name] = true
	}
	return set, rows.Err()
}

// ensureGeneralDomain creates the single mandatory always-on "general" domain if
// it does not already exist. The insert is atomic (guarded by WHERE NOT EXISTS)
// and the idx_one_general unique index makes a concurrent double-seed impossible;
// a race that still trips the constraint is treated as success. No stable_rev
// bump: an empty general domain renders nothing, so the cached block is
// unaffected until a memory is added.
func (s *Store) ensureGeneralDomain(ctx context.Context) error {
	id, err := freshID(ctx, s.db, domainIDPrefix, "domains")
	if err != nil {
		return err
	}
	ts := now()
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO domains(id, agent_id, session_key, type, name, status, version,
		                    summary, state_json, schema_name, schema_version, triggers,
		                    created_at, updated_at)
		SELECT ?,?,?,?,?,?,?,?,?,?,?,?,?,?
		WHERE NOT EXISTS (SELECT 1 FROM domains WHERE type=?)`,
		id, "", "", string(DomainGeneral), "General", string(StatusActive), 1,
		"Global rules, preferences, and standing facts.", "{}", "domain", 1, "", ts, ts,
		string(DomainGeneral))
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint") {
		return nil // another opener seeded it concurrently
	}
	return err
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
