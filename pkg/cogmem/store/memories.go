// ClawEh - Cognitive Memory
// License: MIT

package store

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
)

// AddMemoryParams are the inputs for AddMemory.
type AddMemoryParams struct {
	DomainID           string
	Type               MemoryType
	Text               string
	Status             Status // active or review
	Confidence         float64
	Priority           int
	Source             Source
	Origin             Origin
	SourceSession      *string
	SourceSeqStart     *int64
	SourceSeqEnd       *int64
	SupersedesMemoryID *string
}

// AddMemory inserts a hook, assigns a short id, and bumps stable_rev when the hook
// affects always-on content (always-on domain) or the pending digest (review).
func (s *Store) AddMemory(ctx context.Context, q DBTX, p AddMemoryParams) (Memory, error) {
	if p.Status == "" {
		p.Status = StatusActive
	}
	sticky, err := s.domainSticky(ctx, q, p.DomainID)
	if err != nil {
		return Memory{}, err
	}
	id, err := freshID(ctx, q, memoryIDPrefix, "memories")
	if err != nil {
		return Memory{}, err
	}
	ts := now()
	_, err = q.ExecContext(ctx, `
		INSERT INTO memories(id, domain_id, type, text, status, confidence, priority,
		                  source, origin, source_session, source_seq_start, source_seq_end,
		                  supersedes_memory_id, created_at, updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, p.DomainID, string(p.Type), p.Text, string(p.Status), p.Confidence,
		p.Priority, string(p.Source), string(normalizeOrigin(p.Origin)), p.SourceSession, p.SourceSeqStart,
		p.SourceSeqEnd, p.SupersedesMemoryID, ts, ts)
	if err != nil {
		return Memory{}, fmt.Errorf("cogmem: add hook: %w", err)
	}
	if affectsStable(sticky, p.Status) {
		if err := bumpStableRev(ctx, q); err != nil {
			return Memory{}, err
		}
	}
	return s.GetMemory(ctx, q, id)
}

// RetireMemory marks a hook retired with a reason. It stays in the audit trail.
func (s *Store) RetireMemory(ctx context.Context, q DBTX, id, reason string) error {
	h, err := s.GetMemory(ctx, q, id)
	if err != nil {
		return err
	}
	sticky, err := s.domainSticky(ctx, q, h.DomainID)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx,
		`UPDATE memories SET status=?, retire_reason=?, updated_at=? WHERE id=?`,
		string(StatusRetired), reason, now(), id)
	if err != nil {
		return err
	}
	if affectsStable(sticky, h.Status) {
		return bumpStableRev(ctx, q)
	}
	return nil
}

// DeleteMemory hard-deletes a memory row (no audit trail kept), bumping
// stable_rev when it was active content in a sticky domain. Returns ErrNotFound
// if the memory does not exist.
func (s *Store) DeleteMemory(ctx context.Context, q DBTX, id string) error {
	h, err := s.GetMemory(ctx, q, id)
	if err != nil {
		return err
	}
	sticky, err := s.domainSticky(ctx, q, h.DomainID)
	if err != nil {
		return err
	}
	if _, err := q.ExecContext(ctx, `DELETE FROM memories WHERE id=?`, id); err != nil {
		return fmt.Errorf("cogmem: delete memory: %w", err)
	}
	if affectsStable(sticky, h.Status) {
		return bumpStableRev(ctx, q)
	}
	return nil
}

// SupersedeMemory retires oldID and adds a replacement hook linked back to it.
func (s *Store) SupersedeMemory(ctx context.Context, q DBTX, oldID string, p AddMemoryParams) (Memory, error) {
	if err := s.RetireMemory(ctx, q, oldID, "superseded"); err != nil {
		return Memory{}, err
	}
	p.SupersedesMemoryID = &oldID
	return s.AddMemory(ctx, q, p)
}

// PromoteMemory moves a review hook to active (confirmation).
func (s *Store) PromoteMemory(ctx context.Context, q DBTX, id string) error {
	h, err := s.GetMemory(ctx, q, id)
	if err != nil {
		return err
	}
	if h.Status != StatusReview {
		return nil
	}
	if _, err := q.ExecContext(ctx,
		`UPDATE memories SET status=?, updated_at=? WHERE id=?`,
		string(StatusActive), now(), id); err != nil {
		return err
	}
	return bumpStableRev(ctx, q) // leaves the pending digest, may enter stable
}

// GetMemory loads one hook by id.
func (s *Store) GetMemory(ctx context.Context, q DBTX, id string) (Memory, error) {
	row := q.QueryRowContext(ctx, memorySelect+` WHERE id=?`, id)
	h, err := scanMemory(row)
	if err == sql.ErrNoRows {
		return Memory{}, ErrNotFound
	}
	return h, err
}

// ListMemories returns a domain's hooks with any of the given statuses (all if
// none), ordered by priority desc then id.
func (s *Store) ListMemories(ctx context.Context, q DBTX, domainID string, statuses ...Status) ([]Memory, error) {
	query := memorySelect + ` WHERE domain_id=?`
	args := []any{domainID}
	if len(statuses) > 0 {
		query += ` AND status IN (` + placeholders(len(statuses)) + `)`
		for _, st := range statuses {
			args = append(args, string(st))
		}
	}
	query += ` ORDER BY priority DESC, id`
	return s.queryMemories(ctx, q, query, args...)
}

// SearchMemories does a case-insensitive LIKE scan over active hook text. No FTS5
// (per design); fine at cogmem scale.
func (s *Store) SearchMemories(ctx context.Context, q DBTX, term string, limit int) ([]Memory, error) {
	if limit <= 0 {
		limit = 20
	}
	like := "%" + strings.ToLower(term) + "%"
	return s.queryMemories(ctx, q,
		memorySelect+` WHERE status=? AND lower(text) LIKE ? ORDER BY confidence DESC, id LIMIT ?`,
		string(StatusActive), like, limit)
}

// ListPending returns review-status hooks for the pending-confirmation digest,
// highest confidence first, capped at max.
func (s *Store) ListPending(ctx context.Context, q DBTX, max int) ([]Memory, error) {
	if max <= 0 {
		max = 8
	}
	return s.queryMemories(ctx, q,
		memorySelect+` WHERE status=? ORDER BY confidence DESC, id LIMIT ?`,
		string(StatusReview), max)
}

func (s *Store) queryMemories(ctx context.Context, q DBTX, query string, args ...any) ([]Memory, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Memory
	for rows.Next() {
		h, err := scanMemory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// domainSticky reports whether the domain is sticky (legacy "type" column parsed
// as int > 0).
func (s *Store) domainSticky(ctx context.Context, q DBTX, domainID string) (bool, error) {
	var t string
	err := q.QueryRowContext(ctx, `SELECT type FROM domains WHERE id=?`, domainID).Scan(&t)
	if err == sql.ErrNoRows {
		return false, ErrNotFound
	}
	n, _ := strconv.Atoi(strings.TrimSpace(t))
	return n > 0, err
}

// affectsStable reports whether a memory of this status in a sticky/non-sticky
// domain is part of the cached stable block (sticky active) or the pending
// digest (review) - either way the stable block must be rebuilt.
func affectsStable(sticky bool, st Status) bool {
	return st == StatusReview || (sticky && st == StatusActive)
}

const memorySelect = `
	SELECT id, domain_id, type, text, status, confidence, priority, source, origin,
	       source_session, source_seq_start, source_seq_end, supersedes_memory_id,
	       retire_reason, created_at, updated_at
	FROM memories`

func scanMemory(sc scanner) (Memory, error) {
	var (
		h                            Memory
		kind, status, source, origin string
		createdAt, updatedAt         int64
	)
	err := sc.Scan(&h.ID, &h.DomainID, &kind, &h.Text, &status, &h.Confidence,
		&h.Priority, &source, &origin, &h.SourceSession, &h.SourceSeqStart, &h.SourceSeqEnd,
		&h.SupersedesMemoryID, &h.RetireReason, &createdAt, &updatedAt)
	if err != nil {
		return Memory{}, err
	}
	h.Type = MemoryType(kind)
	h.Status = Status(status)
	h.Source = Source(source)
	h.Origin = normalizeOrigin(Origin(origin))
	h.CreatedAt = timeUnix(createdAt)
	h.UpdatedAt = timeUnix(updatedAt)
	return h, nil
}
