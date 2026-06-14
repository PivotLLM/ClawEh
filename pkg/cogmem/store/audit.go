// ClawEh - Cognitive Memory
// License: MIT

package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/google/uuid"
)

// LogEvent appends an audit row. Generates an id if Event.ID is empty.
func (s *Store) LogEvent(ctx context.Context, q DBTX, e Event) error {
	if e.ID == "" {
		e.ID = uuid.NewString()
	}
	if e.Evidence == "" {
		e.Evidence = "{}"
	}
	_, err := q.ExecContext(ctx, `
		INSERT INTO memory_events(id, event_type, domain_id, hook_id, old_json,
		                          new_json, reason, evidence_json, actor, model,
		                          prompt_hash, created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		e.ID, e.Type, nullStr(e.DomainID), nullStr(e.HookID), nullStr(e.OldJSON),
		nullStr(e.NewJSON), e.Reason, e.Evidence, e.Actor, nullStr(e.Model),
		nullStr(e.PromptHash), now())
	return err
}

// RecordRun writes a consolidation-run debug record (for model comparison).
func (s *Store) RecordRun(ctx context.Context, q DBTX, r Run) error {
	if r.ID == "" {
		r.ID = uuid.NewString()
	}
	var finished *int64
	if r.FinishedAt != nil {
		f := r.FinishedAt.Unix()
		finished = &f
	}
	started := r.StartedAt
	if started.IsZero() {
		started = time.Unix(now(), 0)
	}
	_, err := q.ExecContext(ctx, `
		INSERT INTO consolidation_runs(id, trigger, model, seq_start, seq_end,
		                               input_tokens, output_tokens, status,
		                               ops_applied, error, prompt_hash,
		                               started_at, finished_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.ID, r.Trigger, r.Model, r.SeqStart, r.SeqEnd, r.InputTokens,
		r.OutputTokens, r.Status, r.OpsApplied, nullStr(r.Error),
		nullStr(r.PromptHash), started.Unix(), finished)
	return err
}

// ConsolidationState is the per-archive watermark/trigger bookkeeping.
type ConsolidationState struct {
	ArchivePath     string
	ConsolidatedSeq int64
	LastSeenSeq     int64
	MeaningfulCount int64
	LastRunAt       *time.Time
}

// GetState returns the consolidation state for an archive, zero-valued if absent.
func (s *Store) GetState(ctx context.Context, q DBTX, archivePath string) (ConsolidationState, error) {
	st := ConsolidationState{ArchivePath: archivePath}
	var lastRun *int64
	err := q.QueryRowContext(ctx, `
		SELECT consolidated_seq, last_seen_seq, meaningful_count, last_run_at
		FROM consolidation_state WHERE archive_path=?`, archivePath).
		Scan(&st.ConsolidatedSeq, &st.LastSeenSeq, &st.MeaningfulCount, &lastRun)
	if err == sql.ErrNoRows {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	st.LastRunAt = unixPtr(lastRun)
	return st, nil
}

// SetWatermark records progress after a successful run and resets the meaningful
// counter. lastRun marks the run time.
func (s *Store) SetWatermark(ctx context.Context, q DBTX, archivePath string, consolidatedSeq, lastSeenSeq int64) error {
	_, err := q.ExecContext(ctx, `
		INSERT INTO consolidation_state(archive_path, consolidated_seq, last_seen_seq,
		                                meaningful_count, last_run_at, updated_at)
		VALUES(?,?,?,0,?,?)
		ON CONFLICT(archive_path) DO UPDATE SET
		  consolidated_seq=excluded.consolidated_seq,
		  last_seen_seq=excluded.last_seen_seq,
		  meaningful_count=0,
		  last_run_at=excluded.last_run_at,
		  updated_at=excluded.updated_at`,
		archivePath, consolidatedSeq, lastSeenSeq, now(), now())
	return err
}

// IncMeaningful adds n to the meaningful-message counter (the message trigger).
func (s *Store) IncMeaningful(ctx context.Context, q DBTX, archivePath string, n int) error {
	_, err := q.ExecContext(ctx, `
		INSERT INTO consolidation_state(archive_path, meaningful_count, updated_at)
		VALUES(?,?,?)
		ON CONFLICT(archive_path) DO UPDATE SET
		  meaningful_count = meaningful_count + ?,
		  updated_at = ?`,
		archivePath, n, now(), n, now())
	return err
}

// --- worker leases ---

// AcquireLease grabs a named lease for owner until now+ttl. Returns false if a
// non-expired lease is held by someone else.
func (s *Store) AcquireLease(ctx context.Context, q DBTX, name, owner string, ttl time.Duration) (bool, error) {
	exp := time.Now().Add(ttl).Unix()
	res, err := q.ExecContext(ctx, `
		INSERT INTO worker_leases(name, owner, expires_at) VALUES(?,?,?)
		ON CONFLICT(name) DO UPDATE SET owner=excluded.owner, expires_at=excluded.expires_at
		WHERE worker_leases.expires_at < ? OR worker_leases.owner = excluded.owner`,
		name, owner, exp, now())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ReleaseLease drops a lease held by owner.
func (s *Store) ReleaseLease(ctx context.Context, q DBTX, name, owner string) error {
	_, err := q.ExecContext(ctx, `DELETE FROM worker_leases WHERE name=? AND owner=?`, name, owner)
	return err
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
