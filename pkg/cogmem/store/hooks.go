// ClawEh - Cognitive Memory
// License: MIT

package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// AddHookParams are the inputs for AddHook.
type AddHookParams struct {
	DomainID         string
	Kind             HookKind
	Text             string
	Status           Status // active or review
	Confidence       float64
	Priority         int
	Source           Source
	SourceSession    *string
	SourceSeqStart   *int64
	SourceSeqEnd     *int64
	SupersedesHookID *string
}

// AddHook inserts a hook, assigns a short id, and bumps stable_rev when the hook
// affects always-on content (always-on domain) or the pending digest (review).
func (s *Store) AddHook(ctx context.Context, q DBTX, p AddHookParams) (Hook, error) {
	if p.Status == "" {
		p.Status = StatusActive
	}
	dt, err := s.domainType(ctx, q, p.DomainID)
	if err != nil {
		return Hook{}, err
	}
	id, err := freshID(ctx, q, hookIDPrefix, "hooks")
	if err != nil {
		return Hook{}, err
	}
	ts := now()
	_, err = q.ExecContext(ctx, `
		INSERT INTO hooks(id, domain_id, kind, text, status, confidence, priority,
		                  source, source_session, source_seq_start, source_seq_end,
		                  supersedes_hook_id, created_at, updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, p.DomainID, string(p.Kind), p.Text, string(p.Status), p.Confidence,
		p.Priority, string(p.Source), p.SourceSession, p.SourceSeqStart,
		p.SourceSeqEnd, p.SupersedesHookID, ts, ts)
	if err != nil {
		return Hook{}, fmt.Errorf("cogmem: add hook: %w", err)
	}
	if affectsStable(dt, p.Status) {
		if err := bumpStableRev(ctx, q); err != nil {
			return Hook{}, err
		}
	}
	return s.GetHook(ctx, q, id)
}

// RetireHook marks a hook retired with a reason. It stays in the audit trail.
func (s *Store) RetireHook(ctx context.Context, q DBTX, id, reason string) error {
	h, err := s.GetHook(ctx, q, id)
	if err != nil {
		return err
	}
	dt, err := s.domainType(ctx, q, h.DomainID)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx,
		`UPDATE hooks SET status=?, retire_reason=?, updated_at=? WHERE id=?`,
		string(StatusRetired), reason, now(), id)
	if err != nil {
		return err
	}
	if affectsStable(dt, h.Status) {
		return bumpStableRev(ctx, q)
	}
	return nil
}

// SupersedeHook retires oldID and adds a replacement hook linked back to it.
func (s *Store) SupersedeHook(ctx context.Context, q DBTX, oldID string, p AddHookParams) (Hook, error) {
	if err := s.RetireHook(ctx, q, oldID, "superseded"); err != nil {
		return Hook{}, err
	}
	p.SupersedesHookID = &oldID
	return s.AddHook(ctx, q, p)
}

// PromoteHook moves a review hook to active (confirmation).
func (s *Store) PromoteHook(ctx context.Context, q DBTX, id string) error {
	h, err := s.GetHook(ctx, q, id)
	if err != nil {
		return err
	}
	if h.Status != StatusReview {
		return nil
	}
	if _, err := q.ExecContext(ctx,
		`UPDATE hooks SET status=?, updated_at=? WHERE id=?`,
		string(StatusActive), now(), id); err != nil {
		return err
	}
	return bumpStableRev(ctx, q) // leaves the pending digest, may enter stable
}

// GetHook loads one hook by id.
func (s *Store) GetHook(ctx context.Context, q DBTX, id string) (Hook, error) {
	row := q.QueryRowContext(ctx, hookSelect+` WHERE id=?`, id)
	h, err := scanHook(row)
	if err == sql.ErrNoRows {
		return Hook{}, ErrNotFound
	}
	return h, err
}

// ListHooks returns a domain's hooks with any of the given statuses (all if
// none), ordered by priority desc then id.
func (s *Store) ListHooks(ctx context.Context, q DBTX, domainID string, statuses ...Status) ([]Hook, error) {
	query := hookSelect + ` WHERE domain_id=?`
	args := []any{domainID}
	if len(statuses) > 0 {
		query += ` AND status IN (` + placeholders(len(statuses)) + `)`
		for _, st := range statuses {
			args = append(args, string(st))
		}
	}
	query += ` ORDER BY priority DESC, id`
	return s.queryHooks(ctx, q, query, args...)
}

// SearchHooks does a case-insensitive LIKE scan over active hook text. No FTS5
// (per design); fine at cogmem scale.
func (s *Store) SearchHooks(ctx context.Context, q DBTX, term string, limit int) ([]Hook, error) {
	if limit <= 0 {
		limit = 20
	}
	like := "%" + strings.ToLower(term) + "%"
	return s.queryHooks(ctx, q,
		hookSelect+` WHERE status=? AND lower(text) LIKE ? ORDER BY confidence DESC, id LIMIT ?`,
		string(StatusActive), like, limit)
}

// ListPending returns review-status hooks for the pending-confirmation digest,
// highest confidence first, capped at max.
func (s *Store) ListPending(ctx context.Context, q DBTX, max int) ([]Hook, error) {
	if max <= 0 {
		max = 8
	}
	return s.queryHooks(ctx, q,
		hookSelect+` WHERE status=? ORDER BY confidence DESC, id LIMIT ?`,
		string(StatusReview), max)
}

func (s *Store) queryHooks(ctx context.Context, q DBTX, query string, args ...any) ([]Hook, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Hook
	for rows.Next() {
		h, err := scanHook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func (s *Store) domainType(ctx context.Context, q DBTX, domainID string) (DomainType, error) {
	var t string
	err := q.QueryRowContext(ctx, `SELECT type FROM domains WHERE id=?`, domainID).Scan(&t)
	if err == sql.ErrNoRows {
		return "", ErrNotFound
	}
	return DomainType(t), err
}

// affectsStable reports whether a hook of this status in a domain of this type
// is part of the cached stable block (always-on active) or the pending digest
// (review) - either way the stable block must be rebuilt.
func affectsStable(dt DomainType, st Status) bool {
	return st == StatusReview || (dt.AlwaysOn() && st == StatusActive)
}

const hookSelect = `
	SELECT id, domain_id, kind, text, status, confidence, priority, source,
	       source_session, source_seq_start, source_seq_end, supersedes_hook_id,
	       retire_reason, created_at, updated_at
	FROM hooks`

func scanHook(sc scanner) (Hook, error) {
	var (
		h                    Hook
		kind, status, source string
		createdAt, updatedAt int64
	)
	err := sc.Scan(&h.ID, &h.DomainID, &kind, &h.Text, &status, &h.Confidence,
		&h.Priority, &source, &h.SourceSession, &h.SourceSeqStart, &h.SourceSeqEnd,
		&h.SupersedesHookID, &h.RetireReason, &createdAt, &updatedAt)
	if err != nil {
		return Hook{}, err
	}
	h.Kind = HookKind(kind)
	h.Status = Status(status)
	h.Source = Source(source)
	h.CreatedAt = timeUnix(createdAt)
	h.UpdatedAt = timeUnix(updatedAt)
	return h, nil
}
