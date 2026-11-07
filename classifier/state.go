// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package classifier

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // the "sqlite" database/sql driver

	"github.com/PivotLLM/ClawEh/classifier/classifierfs"
)

// Item statuses in the processed table (design 7.1a).
const (
	StatusClassified   = "classified" // the model answered; the action has not run yet
	StatusActed        = "acted"      // done: the action succeeded, or the category has none
	StatusFailed       = "failed"     // fetch, model or action failed; taken again next run
	StatusUnclassified = "unclassified"
)

// Outcomes of an item handled, in the activity log and Decision.
const (
	OutcomeActed        = "acted"        // the action ran and succeeded
	OutcomeRecorded     = "recorded"     // classified; no action (none for the category, or a dry run)
	OutcomeUnclassified = "unclassified" // the reply named no category twice
	OutcomeFailed       = "failed"       // fetch, model, substitution or action failed
)

// Decision is one item handled: the category and reason the model gave, the
// model, the action tool and how it ended.
type Decision struct {
	ItemID   string    `json:"item_id"`
	Category string    `json:"category,omitempty"`
	Reason   string    `json:"reason,omitempty"`
	Model    string    `json:"model,omitempty"`
	Action   string    `json:"action,omitempty"`
	Outcome  string    `json:"outcome"`
	Error    string    `json:"error,omitempty"`
	DryRun   bool      `json:"dry_run,omitempty"`
	Rerun    bool      `json:"rerun,omitempty"`
	At       time.Time `json:"at"`
}

// ActivityEntry is one activity log row: a Decision and the fields sent to
// the model, each value cut to ActivityValueMax characters.
type ActivityEntry struct {
	Decision
	Fields map[string]string `json:"fields,omitempty"`
}

// RunRecord is a classifier's last run.
type RunRecord struct {
	StartedAt    time.Time `json:"started_at"`
	EndedAt      time.Time `json:"ended_at"`
	Status       string    `json:"status"` // RunOK, RunError or RunStopped
	Error        string    `json:"error,omitempty"`
	Listed       int       `json:"listed"`
	NewItems     int       `json:"new_items"`
	Classified   int       `json:"classified"`
	Acted        int       `json:"acted"`
	Unclassified int       `json:"unclassified"`
	Failed       int       `json:"failed"`
	DryRun       bool      `json:"dry_run,omitempty"`
}

// Run statuses.
const (
	RunOK      = "ok"
	RunError   = "error"   // the run could not list its items, or its model could not be used
	RunStopped = "stopped" // stop_after_failures, or the classifier stopped mid-run
)

// ActivityQuery selects activity entries: newest first, Limit (default
// DefaultActivityLimit, at most MaxActivityLimit) after Offset, optionally
// only those whose fields, item id, category or reason contain Search
// (case-insensitive).
type ActivityQuery struct {
	Limit  int
	Offset int
	Search string
}

// Activity query limits.
const (
	DefaultActivityLimit = 20
	MaxActivityLimit     = 200
)

const stateSchema = `
CREATE TABLE IF NOT EXISTS processed (
  classifier TEXT NOT NULL, item_id TEXT NOT NULL,
  status TEXT NOT NULL,
  category TEXT, reason TEXT, model TEXT,
  classified_at INTEGER, acted_at INTEGER, attempts INTEGER NOT NULL DEFAULT 0,
  error TEXT, updated_at INTEGER NOT NULL, last_seen INTEGER NOT NULL,
  PRIMARY KEY (classifier, item_id));
CREATE TABLE IF NOT EXISTS dry_run (
  classifier TEXT NOT NULL, item_id TEXT NOT NULL, category TEXT, reason TEXT, model TEXT,
  decided_at INTEGER NOT NULL, last_seen INTEGER NOT NULL, PRIMARY KEY (classifier, item_id));
CREATE TABLE IF NOT EXISTS activity (
  id INTEGER PRIMARY KEY, classifier TEXT NOT NULL, at INTEGER NOT NULL, item_id TEXT NOT NULL,
  fields TEXT NOT NULL,
  category TEXT, reason TEXT, model TEXT, action TEXT, outcome TEXT NOT NULL, error TEXT,
  dry_run INTEGER NOT NULL, rerun INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS activity_by_time ON activity (classifier, at DESC);
CREATE TABLE IF NOT EXISTS runs (
  classifier TEXT PRIMARY KEY, started_at INTEGER, ended_at INTEGER, status TEXT, error TEXT,
  listed INTEGER, new_items INTEGER, classified INTEGER, acted INTEGER, unclassified INTEGER, failed INTEGER, dry_run INTEGER);
`

// stateDB is one folder's state.db.
type stateDB struct {
	db   *sql.DB
	path string
}

// stateDSN is the driver DSN (the audit store's flags): WAL, a busy timeout
// so parallel item writes wait for each other, NORMAL sync. readOnly opens it
// without writing (Inspect).
func stateDSN(path string, readOnly bool) string {
	q := []string{"_pragma=busy_timeout(5000)", "_pragma=journal_mode(WAL)", "_pragma=synchronous(NORMAL)"}
	if readOnly {
		q = []string{"mode=ro", "_pragma=busy_timeout(5000)"}
	}
	u := url.URL{Scheme: "file", Path: path, RawQuery: strings.Join(q, "&")}
	return u.String()
}

// openState opens (creating when missing) base's state.db with exactly the
// given modes: the folder (and missing parents) dirMode, the database and
// its -wal and -shm files fileMode, set again on every open.
func openState(ctx context.Context, base string, dirMode, fileMode os.FileMode) (*stateDB, error) {
	if err := mkdirAllExact(base, dirMode); err != nil {
		return nil, fmt.Errorf("create %s: %w", base, err)
	}
	path := filepath.Join(base, classifierfs.StateFile)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, fileMode) // #nosec G304 -- <base>/state.db
	if err != nil {
		return nil, fmt.Errorf("create %s: %w", path, err)
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	if err = os.Chmod(path, fileMode); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", stateDSN(path, false))
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if _, err := db.ExecContext(ctx, stateSchema); err != nil {
		return nil, errors.Join(fmt.Errorf("schema %s: %w", path, err), db.Close())
	}
	for _, side := range []string{"-wal", "-shm"} {
		if err := os.Chmod(path+side, fileMode); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, errors.Join(err, db.Close())
		}
	}
	return &stateDB{db: db, path: path}, nil
}

func (s *stateDB) close() error { return s.db.Close() }

func ms(t time.Time) int64 { return t.UnixMilli() }

func fromMS(v sql.NullInt64) time.Time {
	if !v.Valid {
		return time.Time{}
	}
	return time.UnixMilli(v.Int64).UTC()
}

// processedRow is one item's row in the processed table.
type processedRow struct {
	status   string
	category string
	reason   string
	model    string
	attempts int
}

// processed returns every processed row of classifier cls by item id.
func (s *stateDB) processed(ctx context.Context, cls string) (_ map[string]processedRow, err error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT item_id, status, COALESCE(category,''), COALESCE(reason,''), COALESCE(model,''), attempts FROM processed WHERE classifier = ?`, cls)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	out := map[string]processedRow{}
	for rows.Next() {
		var id string
		var r processedRow
		if err := rows.Scan(&id, &r.status, &r.category, &r.reason, &r.model, &r.attempts); err != nil {
			return nil, err
		}
		out[id] = r
	}
	return out, rows.Err()
}

// setClassified records the model's answer for an item (its action pending).
func (s *stateDB) setClassified(ctx context.Context, cls, item, category, reason, model string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO processed (classifier, item_id, status, category, reason, model, classified_at, acted_at, attempts, error, updated_at, last_seen)
VALUES (?, ?, ?, ?, ?, ?, ?, NULL, 0, NULL, ?, ?)
ON CONFLICT (classifier, item_id) DO UPDATE SET status = excluded.status, category = excluded.category,
  reason = excluded.reason, model = excluded.model, classified_at = excluded.classified_at,
  acted_at = NULL, error = NULL, updated_at = excluded.updated_at`,
		cls, item, StatusClassified, category, reason, model, ms(now), ms(now), ms(now))
	return err
}

// setActed marks an item done. Its row must exist (setClassified wrote it);
// anything but exactly one row changed is an error.
func (s *stateDB) setActed(ctx context.Context, cls, item string, now time.Time) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE processed SET status = ?, acted_at = ?, error = NULL, updated_at = ? WHERE classifier = ? AND item_id = ?`,
		StatusActed, ms(now), ms(now), cls, item)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("mark %s acted: %d rows changed, want 1", item, n)
	}
	return nil
}

// setFailed records a failure for an item, keeping any category it has, and
// counts the attempt.
func (s *stateDB) setFailed(ctx context.Context, cls, item, errText string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO processed (classifier, item_id, status, attempts, error, updated_at, last_seen) VALUES (?, ?, ?, 1, ?, ?, ?)
ON CONFLICT (classifier, item_id) DO UPDATE SET status = excluded.status, attempts = processed.attempts + 1,
  error = excluded.error, updated_at = excluded.updated_at`,
		cls, item, StatusFailed, errText, ms(now), ms(now))
	return err
}

// setUnclassified records that the model named no category for an item.
func (s *stateDB) setUnclassified(ctx context.Context, cls, item, model string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO processed (classifier, item_id, status, category, reason, model, classified_at, attempts, error, updated_at, last_seen)
VALUES (?, ?, ?, NULL, NULL, ?, ?, 0, NULL, ?, ?)
ON CONFLICT (classifier, item_id) DO UPDATE SET status = excluded.status, category = NULL, reason = NULL,
  model = excluded.model, classified_at = excluded.classified_at, acted_at = NULL, error = NULL, updated_at = excluded.updated_at`,
		cls, item, StatusUnclassified, model, ms(now), ms(now), ms(now))
	return err
}

// dryDecided returns the item ids of cls that have a dry-run decision.
func (s *stateDB) dryDecided(ctx context.Context, cls string) (_ map[string]bool, err error) {
	rows, err := s.db.QueryContext(ctx, `SELECT item_id FROM dry_run WHERE classifier = ?`, cls)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// setDry records a dry-run decision.
func (s *stateDB) setDry(ctx context.Context, cls, item, category, reason, model string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO dry_run (classifier, item_id, category, reason, model, decided_at, last_seen) VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (classifier, item_id) DO UPDATE SET category = excluded.category, reason = excluded.reason,
  model = excluded.model, decided_at = excluded.decided_at`,
		cls, item, category, reason, model, ms(now), ms(now))
	return err
}

// markSeen sets last_seen to now for every id of cls that the source lists
// now, in table processed or dry_run, in one statement, so an item is kept
// while it stays listed (design 7.1c: retention runs from when it leaves).
func (s *stateDB) markSeen(ctx context.Context, table, cls string, ids []string, now time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	if table != "processed" && table != "dry_run" {
		return fmt.Errorf("unknown table %s", table)
	}
	for len(ids) > 0 {
		chunk := ids[:min(len(ids), markSeenChunk)]
		ids = ids[len(chunk):]
		args := make([]any, 0, len(chunk)+2)
		args = append(args, ms(now), cls)
		for _, id := range chunk {
			args = append(args, id)
		}
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")
		if _, err := s.db.ExecContext(ctx, `UPDATE `+table+` SET last_seen = ? WHERE classifier = ? AND item_id IN (`+placeholders+`)`, args...); err != nil { // #nosec G202 -- a fixed table name and placeholders
			return err
		}
	}
	return nil
}

// markSeenChunk keeps one statement under SQLite's limit on parameters; a
// list of up to this many ids is marked in one statement.
const markSeenChunk = 10000

// addActivity appends an activity entry; each field value is cut to
// ActivityValueMax characters.
func (s *stateDB) addActivity(ctx context.Context, cls string, e ActivityEntry) error {
	fields := make(map[string]string, len(e.Fields))
	for k, v := range e.Fields {
		fields[k] = cutRunes(v, ActivityValueMax)
	}
	data, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO activity (classifier, at, item_id, fields, category, reason, model, action, outcome, error, dry_run, rerun)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		cls, ms(e.At), e.ItemID, string(data), nullable(e.Category), nullable(e.Reason), nullable(e.Model),
		nullable(e.Action), e.Outcome, nullable(e.Error), e.DryRun, e.Rerun)
	return err
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// activity returns cls's activity entries for q, newest first.
func (s *stateDB) activity(ctx context.Context, cls string, q ActivityQuery) (_ []ActivityEntry, err error) {
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultActivityLimit
	}
	limit = min(limit, MaxActivityLimit)
	offset := max(q.Offset, 0)
	query := `SELECT at, item_id, fields, COALESCE(category,''), COALESCE(reason,''), COALESCE(model,''),
  COALESCE(action,''), outcome, COALESCE(error,''), dry_run, rerun FROM activity WHERE classifier = ?`
	args := []any{cls}
	if search := strings.TrimSpace(q.Search); search != "" {
		pattern := "%" + likeEscaper.Replace(strings.ToLower(search)) + "%"
		query += ` AND (LOWER(fields) LIKE ? ESCAPE '\' OR LOWER(item_id) LIKE ? ESCAPE '\'
  OR LOWER(COALESCE(category,'')) LIKE ? ESCAPE '\' OR LOWER(COALESCE(reason,'')) LIKE ? ESCAPE '\')`
		args = append(args, pattern, pattern, pattern, pattern)
	}
	query += ` ORDER BY at DESC, id DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	var out []ActivityEntry
	for rows.Next() {
		var e ActivityEntry
		var at int64
		var fields string
		if err := rows.Scan(&at, &e.ItemID, &fields, &e.Category, &e.Reason, &e.Model, &e.Action,
			&e.Outcome, &e.Error, &e.DryRun, &e.Rerun); err != nil {
			return nil, err
		}
		e.At = time.UnixMilli(at).UTC()
		if err := json.Unmarshal([]byte(fields), &e.Fields); err != nil {
			e.Fields = nil
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// prune deletes cls's processed and dry-run rows whose item the source last
// listed before processedBefore, and its activity before activityBefore. A
// zero time prunes nothing of that kind.
func (s *stateDB) prune(ctx context.Context, cls string, processedBefore, activityBefore time.Time) error {
	for _, q := range []struct {
		sql string
		at  time.Time
	}{
		{`DELETE FROM processed WHERE classifier = ? AND last_seen < ?`, processedBefore},
		{`DELETE FROM dry_run WHERE classifier = ? AND last_seen < ?`, processedBefore},
		{`DELETE FROM activity WHERE classifier = ? AND at < ?`, activityBefore},
	} {
		if q.at.IsZero() {
			continue
		}
		if _, err := s.db.ExecContext(ctx, q.sql, cls, ms(q.at)); err != nil {
			return err
		}
	}
	return nil
}

// forget removes every row of cls.
func (s *stateDB) forget(ctx context.Context, cls string) error {
	for _, table := range []string{"processed", "dry_run", "activity", "runs"} {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM `+table+` WHERE classifier = ?`, cls); err != nil { // #nosec G202 -- a fixed table name
			return err
		}
	}
	return nil
}

// setRun records cls's last run.
func (s *stateDB) setRun(ctx context.Context, cls string, r RunRecord) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO runs (classifier, started_at, ended_at, status, error, listed, new_items, classified, acted, unclassified, failed, dry_run)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (classifier) DO UPDATE SET started_at = excluded.started_at, ended_at = excluded.ended_at,
  status = excluded.status, error = excluded.error, listed = excluded.listed, new_items = excluded.new_items,
  classified = excluded.classified, acted = excluded.acted, unclassified = excluded.unclassified,
  failed = excluded.failed, dry_run = excluded.dry_run`,
		cls, ms(r.StartedAt), ms(r.EndedAt), r.Status, nullable(r.Error), r.Listed, r.NewItems, r.Classified,
		r.Acted, r.Unclassified, r.Failed, r.DryRun)
	return err
}

// lastRunFrom returns cls's last run in db, or nil when it has none.
func lastRunFrom(ctx context.Context, db *sql.DB, cls string) (*RunRecord, error) {
	var r RunRecord
	var started, ended sql.NullInt64
	var errText sql.NullString
	err := db.QueryRowContext(ctx, `SELECT started_at, ended_at, status, error, listed, new_items, classified, acted,
  unclassified, failed, dry_run FROM runs WHERE classifier = ?`, cls).Scan(&started, &ended, &r.Status, &errText,
		&r.Listed, &r.NewItems, &r.Classified, &r.Acted, &r.Unclassified, &r.Failed, &r.DryRun)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r.StartedAt, r.EndedAt, r.Error = fromMS(started), fromMS(ended), errText.String
	return &r, nil
}

// counts returns the number of cls's processed items by status.
func (s *stateDB) counts(ctx context.Context, cls string) (_ map[string]int, err error) {
	rows, err := s.db.QueryContext(ctx, `SELECT status, COUNT(*) FROM processed WHERE classifier = ? GROUP BY status`, cls)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	out := map[string]int{}
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return nil, err
		}
		out[status] = n
	}
	return out, rows.Err()
}
