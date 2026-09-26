// ClawEh
// License: MIT

// Package audit is the append-only record of who did what: every agent tool
// call, every configuration write through the WebUI, and every authentication
// event. Rows live in <CLAW_HOME>/audit.db (SQLite, WAL, mode 0600) and are
// never updated or deleted except by the retention prune.
//
// Recording is asynchronous: Record hands the event to a buffered channel that
// a single writer goroutine drains, so a slow disk never stalls an agent turn.
// When the buffer is full the event is dropped and counted rather than blocking.
package audit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"

	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/utils"
)

// Event kinds.
const (
	// KindToolCall is one agent tool execution.
	KindToolCall = "tool_call"
	// KindConfigWrite is one save of the configuration through the API.
	KindConfigWrite = "config_write"
	// KindAuth is a login, logout or lockout on the WebUI.
	KindAuth = "auth"
)

// Outcome values.
const (
	OutcomeOK    = "ok"
	OutcomeError = "error"
)

const (
	// FileName is the database file inside the data directory.
	FileName = "audit.db"
	// RetentionDays is how long rows are kept before the daily prune removes
	// them. Follow-up: make this the `audit.retention_days` config key.
	RetentionDays = 90
	// DefaultBufferSize is how many events may wait for the writer before
	// Record starts dropping.
	DefaultBufferSize = 1024
	// MaxDetailsBytes caps the details column; longer values are truncated.
	MaxDetailsBytes = 2048
	// MaxQueryLimit is the most rows one Query returns.
	MaxQueryLimit = 1000
	// DefaultQueryLimit applies when a Filter leaves Limit at zero.
	DefaultQueryLimit = 100

	writeBatchMax   = 64
	writeTimeout    = 10 * time.Second
	pruneInterval   = 24 * time.Hour
	dropWarnEvery   = time.Minute
	flushPollPeriod = 5 * time.Millisecond
)

// ErrClosed is returned by Query on a closed store.
var ErrClosed = errors.New("audit: store is closed")

// Event is one audit row. Sender is the chat sender for a tool call and the
// client IP for an HTTP-originated event; Actor is the authenticated operator
// (empty for agent activity). Details is JSON and is capped at MaxDetailsBytes.
type Event struct {
	ID         int64     `json:"id"`
	TS         time.Time `json:"ts"`
	Kind       string    `json:"kind"`
	Actor      string    `json:"actor,omitempty"`
	Session    string    `json:"session,omitempty"`
	Channel    string    `json:"channel,omitempty"`
	Sender     string    `json:"sender,omitempty"`
	Agent      string    `json:"agent,omitempty"`
	Tool       string    `json:"tool,omitempty"`
	Summary    string    `json:"summary,omitempty"`
	Details    string    `json:"details,omitempty"`
	Outcome    string    `json:"outcome,omitempty"`
	DurationMS int64     `json:"duration_ms"`
	TurnID     string    `json:"turn_id,omitempty"`
}

// Filter selects rows for Query. Zero values mean "no constraint". Rows come
// back newest first; BeforeID pages further back (rows with a smaller id).
type Filter struct {
	Since    time.Time
	Until    time.Time
	Kind     string
	Agent    string
	Session  string
	Limit    int
	BeforeID int64
}

// Store is the audit database plus its writer and prune goroutines.
type Store struct {
	db    *sql.DB
	path  string
	queue chan Event

	// pending counts events accepted by Record and not yet written, so Flush
	// can tell when the writer has caught up.
	pending  atomic.Int64
	dropped  atomic.Uint64
	lastWarn atomic.Int64 // UnixNano of the last buffer-full warning

	closeMu sync.RWMutex
	closed  bool
	done    chan struct{}
	wg      sync.WaitGroup

	retention time.Duration
	now       func() time.Time
}

// Option configures Open.
type Option func(*Store)

// WithBufferSize sets the Record queue depth (default DefaultBufferSize).
func WithBufferSize(n int) Option {
	return func(s *Store) {
		if n > 0 {
			s.queue = make(chan Event, n)
		}
	}
}

// WithRetention overrides the prune horizon (default RetentionDays).
func WithRetention(d time.Duration) Option {
	return func(s *Store) {
		if d > 0 {
			s.retention = d
		}
	}
}

// withClock replaces the store's clock; tests use it to age rows.
func withClock(now func() time.Time) Option {
	return func(s *Store) { s.now = now }
}

const schema = `
CREATE TABLE IF NOT EXISTS events (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  ts          INTEGER NOT NULL,
  kind        TEXT    NOT NULL,
  actor       TEXT    NOT NULL DEFAULT '',
  session     TEXT    NOT NULL DEFAULT '',
  channel     TEXT    NOT NULL DEFAULT '',
  sender      TEXT    NOT NULL DEFAULT '',
  agent       TEXT    NOT NULL DEFAULT '',
  tool        TEXT    NOT NULL DEFAULT '',
  summary     TEXT    NOT NULL DEFAULT '',
  details     TEXT    NOT NULL DEFAULT '',
  outcome     TEXT    NOT NULL DEFAULT '',
  duration_ms INTEGER NOT NULL DEFAULT 0,
  turn_id     TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_events_ts      ON events(ts);
CREATE INDEX IF NOT EXISTS idx_events_kind    ON events(kind, id);
CREATE INDEX IF NOT EXISTS idx_events_agent   ON events(agent, id);
CREATE INDEX IF NOT EXISTS idx_events_session ON events(session, id);
`

// Open creates or opens the audit database at path. The parent directory is
// created 0700 when missing and the file is forced to 0600 (SQLite gives the
// WAL and shm companions the database file's mode).
func Open(ctx context.Context, path string, opts ...Option) (*Store, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("audit: resolve %s: %w", path, err)
	}
	if mkErr := os.MkdirAll(filepath.Dir(abs), 0o700); mkErr != nil {
		return nil, fmt.Errorf("audit: create directory: %w", mkErr)
	}
	// Create the file ourselves so the mode is 0600 from the first byte; the
	// chmod covers a database created by an earlier run under a laxer umask.
	f, err := os.OpenFile(abs, os.O_RDWR|os.O_CREATE, 0o600) //nolint:gosec // path is <CLAW_HOME>/audit.db
	if err != nil {
		return nil, fmt.Errorf("audit: create %s: %w", abs, err)
	}
	utils.CloseQuietly(f)
	if chErr := os.Chmod(abs, 0o600); chErr != nil {
		return nil, fmt.Errorf("audit: chmod %s: %w", abs, chErr)
	}

	db, err := sql.Open("sqlite", dsn(abs))
	if err != nil {
		return nil, fmt.Errorf("audit: open %s: %w", abs, err)
	}
	if _, err := db.ExecContext(ctx, schema); err != nil {
		utils.CloseQuietly(db)
		return nil, fmt.Errorf("audit: schema: %w", err)
	}

	s := &Store{
		db:        db,
		path:      abs,
		queue:     make(chan Event, DefaultBufferSize),
		done:      make(chan struct{}),
		retention: RetentionDays * 24 * time.Hour,
		now:       time.Now,
	}
	for _, o := range opts {
		o(s)
	}

	s.wg.Add(2)
	go s.writer() //nolint:contextcheck // the writer runs until Close, not for the life of Open's ctx; each batch gets its own bounded write timeout
	go s.pruner() //nolint:contextcheck,gosec // G118: the pruner runs until Close, not for the life of Open's ctx; each prune gets its own bounded timeout
	return s, nil
}

// dsn builds the driver DSN: WAL for concurrent readers, a busy timeout so a
// prune and a write never fail each other, and NORMAL sync (durable to the OS
// on every commit, fsync at checkpoints — the right trade for an audit trail
// that must not slow the caller).
func dsn(abs string) string {
	u := url.URL{
		Scheme: "file",
		Path:   abs,
		RawQuery: strings.Join([]string{
			"_pragma=busy_timeout(5000)",
			"_pragma=journal_mode(WAL)",
			"_pragma=synchronous(NORMAL)",
		}, "&"),
	}
	return u.String()
}

// Path returns the database file.
func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// Record queues e for writing and returns immediately. It never blocks: when
// the queue is full the event is dropped, counted (see Dropped) and a warning
// is logged at most once a minute. Safe on a nil or closed store (no-op).
func (s *Store) Record(e Event) {
	if s == nil {
		return
	}
	if e.TS.IsZero() {
		e.TS = s.now()
	}
	e.Details = TruncateDetails(e.Details)

	s.closeMu.RLock()
	defer s.closeMu.RUnlock()
	if s.closed {
		return
	}
	s.pending.Add(1)
	select {
	case s.queue <- e:
	default:
		s.pending.Add(-1)
		s.noteDrop()
	}
}

func (s *Store) noteDrop() {
	n := s.dropped.Add(1)
	now := s.now().UnixNano()
	last := s.lastWarn.Load()
	if now-last < int64(dropWarnEvery) {
		return
	}
	if s.lastWarn.CompareAndSwap(last, now) {
		logger.WarnCF("audit", "audit event dropped: write queue is full",
			map[string]any{"dropped_total": n, "queue_size": cap(s.queue)})
	}
}

// Dropped is the number of events Record has discarded because the queue was
// full since the store was opened.
func (s *Store) Dropped() uint64 {
	if s == nil {
		return 0
	}
	return s.dropped.Load()
}

// TruncateDetails caps s at MaxDetailsBytes on a rune boundary, marking the cut.
func TruncateDetails(s string) string {
	if len(s) <= MaxDetailsBytes {
		return s
	}
	const marker = "…(truncated)"
	cut := MaxDetailsBytes - len(marker)
	for cut > 0 && !utf8RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + marker
}

func utf8RuneStart(b byte) bool { return b&0xC0 != 0x80 }

// writer drains the queue, batching whatever is waiting into one transaction.
func (s *Store) writer() {
	defer s.wg.Done()
	for e, ok := <-s.queue; ok; e, ok = <-s.queue {
		batch := []Event{e}
	drain:
		for len(batch) < writeBatchMax {
			select {
			case more, ok := <-s.queue:
				if !ok {
					break drain
				}
				batch = append(batch, more)
			default:
				break drain
			}
		}
		if err := s.insert(batch); err != nil {
			logger.WarnCF("audit", "audit write failed; events lost",
				map[string]any{"error": err.Error(), "count": len(batch)})
		}
		s.pending.Add(-int64(len(batch)))
	}
}

func (s *Store) insert(batch []Event) error {
	ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO events
		(ts, kind, actor, session, channel, sender, agent, tool, summary, details, outcome, duration_ms, turn_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := stmt.Close(); closeErr != nil {
			logger.DebugCF("audit", "statement close failed", map[string]any{"error": closeErr.Error()})
		}
	}()
	for _, e := range batch {
		if _, err := stmt.ExecContext(ctx, e.TS.UnixMilli(), e.Kind, e.Actor, e.Session, e.Channel,
			e.Sender, e.Agent, e.Tool, e.Summary, e.Details, e.Outcome, e.DurationMS, e.TurnID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func rollback(tx *sql.Tx) {
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		logger.WarnCF("audit", "transaction rollback failed", map[string]any{"error": err.Error()})
	}
}

// pruner deletes rows older than the retention horizon once at start and then
// daily.
func (s *Store) pruner() {
	defer s.wg.Done()
	ticker := time.NewTicker(pruneInterval)
	defer ticker.Stop()
	for {
		ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
		if n, err := s.Prune(ctx, s.now().Add(-s.retention)); err != nil {
			logger.WarnCF("audit", "audit prune failed", map[string]any{"error": err.Error()})
		} else if n > 0 {
			logger.InfoCF("audit", "audit rows pruned", map[string]any{"rows": n, "retention_days": int(s.retention.Hours() / 24)})
		}
		cancel()
		select {
		case <-s.done:
			return
		case <-ticker.C:
		}
	}
}

// Prune deletes every row recorded before the given time and reports how many.
func (s *Store) Prune(ctx context.Context, before time.Time) (int64, error) {
	if s == nil {
		return 0, nil
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM events WHERE ts < ?`, before.UnixMilli())
	if err != nil {
		return 0, fmt.Errorf("audit: prune: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("audit: prune count: %w", err)
	}
	return n, nil
}

// Flush blocks until every event accepted by Record has been written, or ctx
// ends. It exists for tests and shutdown; callers on the request path never
// need it.
func (s *Store) Flush(ctx context.Context) error {
	if s == nil {
		return nil
	}
	for s.pending.Load() > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(flushPollPeriod):
		}
	}
	return nil
}

// Query returns rows matching f, newest first. Limit is clamped to
// [1, MaxQueryLimit] with DefaultQueryLimit for zero.
func (s *Store) Query(ctx context.Context, f Filter) ([]Event, error) {
	if s == nil {
		return nil, ErrClosed
	}
	s.closeMu.RLock()
	closed := s.closed
	s.closeMu.RUnlock()
	if closed {
		return nil, ErrClosed
	}

	where := []string{"1=1"}
	args := []any{}
	if !f.Since.IsZero() {
		where = append(where, "ts >= ?")
		args = append(args, f.Since.UnixMilli())
	}
	if !f.Until.IsZero() {
		where = append(where, "ts <= ?")
		args = append(args, f.Until.UnixMilli())
	}
	if f.Kind != "" {
		where = append(where, "kind = ?")
		args = append(args, f.Kind)
	}
	if f.Agent != "" {
		where = append(where, "agent = ?")
		args = append(args, f.Agent)
	}
	if f.Session != "" {
		where = append(where, "session = ?")
		args = append(args, f.Session)
	}
	if f.BeforeID > 0 {
		where = append(where, "id < ?")
		args = append(args, f.BeforeID)
	}
	limit := f.Limit
	switch {
	case limit <= 0:
		limit = DefaultQueryLimit
	case limit > MaxQueryLimit:
		limit = MaxQueryLimit
	}
	args = append(args, limit)

	const columns = `id, ts, kind, actor, session, channel, sender, agent, tool, summary, details, outcome, duration_ms, turn_id`
	q := `SELECT ` + columns + ` FROM events WHERE ` + strings.Join(where, " AND ") + ` ORDER BY id DESC LIMIT ?` //nolint:gosec // G202: where holds fixed column predicates with ? placeholders; every value travels in args
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("audit: query: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			logger.DebugCF("audit", "rows close failed", map[string]any{"error": closeErr.Error()})
		}
	}()

	out := make([]Event, 0, limit)
	for rows.Next() {
		var e Event
		var ms int64
		if err := rows.Scan(&e.ID, &ms, &e.Kind, &e.Actor, &e.Session, &e.Channel, &e.Sender, &e.Agent,
			&e.Tool, &e.Summary, &e.Details, &e.Outcome, &e.DurationMS, &e.TurnID); err != nil {
			return nil, fmt.Errorf("audit: scan: %w", err)
		}
		e.TS = time.UnixMilli(ms).UTC()
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("audit: rows: %w", err)
	}
	return out, nil
}

// Close stops accepting events, writes what is queued, stops the prune job and
// closes the database. Safe on a nil store and idempotent.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.closeMu.Lock()
	if s.closed {
		s.closeMu.Unlock()
		return nil
	}
	s.closed = true
	close(s.queue)
	close(s.done)
	s.closeMu.Unlock()

	s.wg.Wait()
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("audit: close: %w", err)
	}
	return nil
}
