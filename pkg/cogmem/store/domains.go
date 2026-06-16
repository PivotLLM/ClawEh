// ClawEh - Cognitive Memory
// License: MIT

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrNotFound is returned when an addressed domain or hook does not exist.
var ErrNotFound = errors.New("cogmem: not found")

// ErrVersionConflict is returned by UpdateDomain when expected_version does not
// match the stored version (optimistic concurrency).
var ErrVersionConflict = errors.New("cogmem: version conflict")

// CreateDomainParams are the inputs for CreateDomain.
type CreateDomainParams struct {
	AgentID    string
	SessionKey string
	Type       DomainType
	Name       string
	Status     Status // active or review
	Summary    string
	State      DomainState
	Triggers   string // comma-delimited tool-name substrings (optional)
}

// CreateDomain inserts a new domain, assigns a short id, and bumps stable_rev
// (a new domain always affects either the index or the pending digest).
func (s *Store) CreateDomain(ctx context.Context, q DBTX, p CreateDomainParams) (Domain, error) {
	id, err := freshID(ctx, q, domainIDPrefix, "domains")
	if err != nil {
		return Domain{}, err
	}
	if p.Status == "" {
		p.Status = StatusActive
	}
	stateJSON, err := json.Marshal(p.State)
	if err != nil {
		return Domain{}, err
	}
	ts := now()
	_, err = q.ExecContext(ctx, `
		INSERT INTO domains(id, agent_id, session_key, type, name, status, version,
		                    summary, state_json, schema_name, schema_version,
		                    last_active_at, triggers, created_at, updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, p.AgentID, p.SessionKey, string(p.Type), p.Name, string(p.Status), 1,
		p.Summary, string(stateJSON), "domain", 1, ts, normalizeTriggers(p.Triggers), ts, ts)
	if err != nil {
		return Domain{}, fmt.Errorf("cogmem: create domain: %w", err)
	}
	if err := bumpStableRev(ctx, q); err != nil {
		return Domain{}, err
	}
	return s.GetDomain(ctx, q, id, false)
}

// UpdateDomainParams patches a domain under optimistic concurrency.
type UpdateDomainParams struct {
	ExpectedVersion int64
	Summary         *string
	State           *DomainState
	Status          *Status
	Triggers        *string // when non-nil, replaces the comma-delimited trigger list
}

// UpdateDomain applies a patch if ExpectedVersion matches, bumping version and
// (when index/always-on content changed) stable_rev. Returns ErrVersionConflict
// or ErrNotFound.
func (s *Store) UpdateDomain(ctx context.Context, q DBTX, id string, p UpdateDomainParams) error {
	cur, err := s.GetDomain(ctx, q, id, false)
	if err != nil {
		return err
	}
	if cur.Version != p.ExpectedVersion {
		return ErrVersionConflict
	}
	summary := cur.Summary
	if p.Summary != nil {
		summary = *p.Summary
	}
	state := cur.State
	if p.State != nil {
		state = *p.State
	}
	status := cur.Status
	if p.Status != nil {
		status = *p.Status
	}
	triggers := cur.Triggers
	if p.Triggers != nil {
		triggers = normalizeTriggers(*p.Triggers)
	}
	stateJSON, err := json.Marshal(state)
	if err != nil {
		return err
	}
	res, err := q.ExecContext(ctx, `
		UPDATE domains SET summary=?, state_json=?, status=?, triggers=?, version=version+1, updated_at=?
		WHERE id=? AND version=?`,
		summary, string(stateJSON), string(status), triggers, now(), id, p.ExpectedVersion)
	if err != nil {
		return fmt.Errorf("cogmem: update domain: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrVersionConflict
	}
	// Index/pending/always-on visibility changed if summary, status, or an
	// always-on domain changed.
	if cur.Type.AlwaysOn() || p.Summary != nil || p.Status != nil {
		if err := bumpStableRev(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

// ArchiveDomain marks a domain archived (out of default prompting).
func (s *Store) ArchiveDomain(ctx context.Context, q DBTX, id string) error {
	res, err := q.ExecContext(ctx, `
		UPDATE domains SET status=?, archived_at=?, version=version+1, updated_at=?
		WHERE id=? AND status!=?`,
		string(StatusArchived), now(), now(), id, string(StatusArchived))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Either missing or already archived.
		if _, gerr := s.GetDomain(ctx, q, id, false); gerr != nil {
			return gerr
		}
		return nil
	}
	return bumpStableRev(ctx, q)
}

// Touch updates last_active_at (recency signal for routed pre-load). It does NOT
// bump stable_rev — recency is not part of the cached stable block.
func (s *Store) Touch(ctx context.Context, q DBTX, id string) error {
	_, err := q.ExecContext(ctx, `UPDATE domains SET last_active_at=? WHERE id=?`, nowNano(), id)
	return err
}

// GetDomain loads one domain by id, optionally with its hooks.
func (s *Store) GetDomain(ctx context.Context, q DBTX, id string, withHooks bool) (Domain, error) {
	row := q.QueryRowContext(ctx, domainSelect+` WHERE id=?`, id)
	d, err := scanDomain(row)
	if err == sql.ErrNoRows {
		return Domain{}, ErrNotFound
	}
	if err != nil {
		return Domain{}, err
	}
	if withHooks {
		hooks, err := s.ListHooks(ctx, q, id, StatusActive)
		if err != nil {
			return Domain{}, err
		}
		d.Hooks = hooks
	}
	return d, nil
}

// GeneralDomain returns the mandatory always-on "general" domain, which is
// seeded on Open so it always exists.
func (s *Store) GeneralDomain(ctx context.Context, q DBTX) (Domain, error) {
	row := q.QueryRowContext(ctx, domainSelect+` WHERE type=? ORDER BY id LIMIT 1`, string(DomainGeneral))
	d, err := scanDomain(row)
	if err == sql.ErrNoRows {
		return Domain{}, ErrNotFound
	}
	return d, err
}

// ListDomains returns domains with any of the given statuses (all if none),
// ordered by id for stable index rendering.
func (s *Store) ListDomains(ctx context.Context, q DBTX, statuses ...Status) ([]Domain, error) {
	query := domainSelect
	var args []any
	if len(statuses) > 0 {
		query += ` WHERE status IN (` + placeholders(len(statuses)) + `)`
		for _, st := range statuses {
			args = append(args, string(st))
		}
	}
	query += ` ORDER BY id`
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Domain
	for rows.Next() {
		d, err := scanDomain(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

const domainSelect = `
	SELECT id, agent_id, session_key, type, name, status, version, summary,
	       state_json, schema_name, schema_version, last_active_at, triggers,
	       created_at, updated_at, archived_at
	FROM domains`

type scanner interface {
	Scan(dest ...any) error
}

// normalizeTriggers canonicalizes a comma-delimited trigger list: trims each
// token, drops empties, lowercases (matching is case-insensitive), and rejoins
// with single commas. Stored form is what TriggerTokens splits back out.
func normalizeTriggers(s string) string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.ToLower(strings.TrimSpace(p)); t != "" {
			out = append(out, t)
		}
	}
	return strings.Join(out, ",")
}

// TriggerTokens returns the domain's normalized trigger substrings (lowercased,
// no empties). A domain activates when an invoked tool name contains any token.
func (d Domain) TriggerTokens() []string {
	if d.Triggers == "" {
		return nil
	}
	return strings.Split(normalizeTriggers(d.Triggers), ",")
}

// MatchTrigger reports the first trigger token contained (case-insensitively) in
// toolName, and whether any matched. toolName need not be pre-lowercased.
func (d Domain) MatchTrigger(toolName string) (string, bool) {
	lname := strings.ToLower(toolName)
	for _, t := range d.TriggerTokens() {
		if strings.Contains(lname, t) {
			return t, true
		}
	}
	return "", false
}

func scanDomain(sc scanner) (Domain, error) {
	var (
		d                         Domain
		typ, status, stateJSON    string
		createdAt, updatedAt      int64
		lastActivePtr, archivedAt *int64
	)
	err := sc.Scan(&d.ID, &d.AgentID, &d.SessionKey, &typ, &d.Name, &status,
		&d.Version, &d.Summary, &stateJSON, &d.SchemaName, &d.SchemaVersion,
		&lastActivePtr, &d.Triggers, &createdAt, &updatedAt, &archivedAt)
	if err != nil {
		return Domain{}, err
	}
	d.Type = DomainType(typ)
	d.Status = Status(status)
	if err := json.Unmarshal([]byte(stateJSON), &d.State); err != nil {
		return Domain{}, fmt.Errorf("cogmem: bad state_json for %s: %w", d.ID, err)
	}
	d.LastActiveAt = derefOr0(lastActivePtr)
	d.CreatedAt = timeUnix(createdAt)
	d.UpdatedAt = timeUnix(updatedAt)
	d.ArchivedAt = unixPtr(archivedAt)
	return d, nil
}
