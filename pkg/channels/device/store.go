package device

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// ErrPendingNotFound is returned when an approve/reject references an unknown request.
var ErrPendingNotFound = errors.New("device: pending pairing not found")

// ErrPairedNotFound is returned when an update references an unknown paired device.
var ErrPairedNotFound = errors.New("device: paired device not found")

// PairedDevice is an approved device identity (auth/identity, not command surface).
type PairedDevice struct {
	DeviceID     string
	PublicKey    string
	DisplayName  string
	Platform     string
	DeviceFamily string
	ClientID     string
	ClientMode   string
	Roles        []string
	Scopes       []string // approved scope baseline
	AgentID      string   // per-device assigned agent ("" = gateway default)
	CreatedAtMs  int64
	ApprovedAtMs int64
	LastSeenAtMs int64
}

// DeviceToken is a persistent bearer token issued per granted role on approval.
type DeviceToken struct {
	Token       string
	DeviceID    string
	Role        string
	Scopes      []string
	CreatedAtMs int64
	RevokedAtMs int64
}

// PendingPairing is an unapproved device awaiting operator approval.
type PendingPairing struct {
	RequestID    string
	DeviceID     string
	PublicKey    string
	DisplayName  string
	Platform     string
	DeviceFamily string
	ClientID     string
	ClientMode   string
	Role         string
	Scopes       []string // requested
	RemoteIP     string
	CreatedAtMs  int64
}

// Store owns the gateway device-pairing database (paired devices, tokens, pendings).
type Store struct {
	db *sql.DB
}

const deviceSchema = `
CREATE TABLE IF NOT EXISTS paired_devices (
  device_id       TEXT PRIMARY KEY,
  public_key      TEXT NOT NULL,
  display_name    TEXT,
  platform        TEXT,
  device_family   TEXT,
  client_id       TEXT,
  client_mode     TEXT,
  roles           TEXT,
  scopes          TEXT,
  agent_id        TEXT NOT NULL DEFAULT '',
  created_at_ms   INTEGER NOT NULL,
  approved_at_ms  INTEGER NOT NULL,
  last_seen_at_ms INTEGER
);
CREATE TABLE IF NOT EXISTS device_tokens (
  token         TEXT PRIMARY KEY,
  device_id     TEXT NOT NULL,
  role          TEXT NOT NULL,
  scopes        TEXT,
  created_at_ms INTEGER NOT NULL,
  revoked_at_ms INTEGER
);
CREATE INDEX IF NOT EXISTS idx_device_tokens_device ON device_tokens(device_id);
CREATE TABLE IF NOT EXISTS pending_pairings (
  request_id    TEXT PRIMARY KEY,
  device_id     TEXT NOT NULL,
  public_key    TEXT NOT NULL,
  display_name  TEXT,
  platform      TEXT,
  device_family TEXT,
  client_id     TEXT,
  client_mode   TEXT,
  role          TEXT,
  scopes        TEXT,
  remote_ip     TEXT,
  created_at_ms INTEGER NOT NULL
);
`

// OpenStore opens (or creates) the device pairing DB with WAL mode (pure-Go sqlite).
func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("device: open %s: %w", path, err)
	}
	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA foreign_keys=ON",
		"PRAGMA synchronous=NORMAL",
	}
	for _, p := range pragmas {
		if _, err := db.ExecContext(context.Background(), p); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("device: %q: %w", p, err)
		}
	}
	if _, err := db.ExecContext(context.Background(), deviceSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("device: schema: %w", err)
	}
	// Migration for DBs created before agent_id existed. ADD COLUMN fails with a
	// "duplicate column" error once applied, which is the steady state — ignore it.
	if _, err := db.ExecContext(context.Background(),
		`ALTER TABLE paired_devices ADD COLUMN agent_id TEXT NOT NULL DEFAULT ''`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column") {
		_ = db.Close()
		return nil, fmt.Errorf("device: migrate agent_id: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func nowMs() int64 { return time.Now().UnixMilli() }

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func marshalStrings(ss []string) string {
	if len(ss) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(ss)
	return string(b)
}

func unmarshalStrings(s string) []string {
	if s == "" {
		return nil
	}
	var ss []string
	_ = json.Unmarshal([]byte(s), &ss)
	return ss
}

// CreatePending records a pending pairing, replacing any prior pending for the same
// device id (one pending per device). The request id is STABLE across retries: a
// device's reconnect loop re-creates its pending every few seconds, so reusing the
// existing request id keeps it valid for an operator about to approve it (otherwise
// approve-by-id races the device and fails "not found"). Returns the request id.
func (s *Store) CreatePending(ctx context.Context, p PendingPairing) (string, error) {
	if p.CreatedAtMs == 0 {
		p.CreatedAtMs = nowMs()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()

	var existingID string
	if qerr := tx.QueryRowContext(ctx,
		`SELECT request_id FROM pending_pairings WHERE device_id=?`, p.DeviceID).Scan(&existingID); qerr != nil && !errors.Is(qerr, sql.ErrNoRows) {
		return "", qerr
	}
	switch {
	case existingID != "":
		p.RequestID = existingID // reuse so the id stays stable across reconnects
	case p.RequestID == "":
		id, gerr := randomHex(16)
		if gerr != nil {
			return "", gerr
		}
		p.RequestID = id
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM pending_pairings WHERE device_id=?`, p.DeviceID); err != nil {
		return "", err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO pending_pairings
		(request_id, device_id, public_key, display_name, platform, device_family, client_id, client_mode, role, scopes, remote_ip, created_at_ms)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		p.RequestID, p.DeviceID, p.PublicKey, p.DisplayName, p.Platform, p.DeviceFamily,
		p.ClientID, p.ClientMode, p.Role, marshalStrings(p.Scopes), p.RemoteIP, p.CreatedAtMs)
	if err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return p.RequestID, nil
}

// ListPending returns all pending pairing requests, newest first.
func (s *Store) ListPending(ctx context.Context) ([]PendingPairing, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT request_id, device_id, public_key, display_name,
		platform, device_family, client_id, client_mode, role, scopes, remote_ip, created_at_ms
		FROM pending_pairings ORDER BY created_at_ms DESC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []PendingPairing
	for rows.Next() {
		var p PendingPairing
		var scopes string
		if err := rows.Scan(&p.RequestID, &p.DeviceID, &p.PublicKey, &p.DisplayName, &p.Platform,
			&p.DeviceFamily, &p.ClientID, &p.ClientMode, &p.Role, &scopes, &p.RemoteIP, &p.CreatedAtMs); err != nil {
			return nil, err
		}
		p.Scopes = unmarshalStrings(scopes)
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) getPending(ctx context.Context, requestID string) (*PendingPairing, error) {
	var p PendingPairing
	var scopes string
	err := s.db.QueryRowContext(ctx, `SELECT request_id, device_id, public_key, display_name,
		platform, device_family, client_id, client_mode, role, scopes, remote_ip, created_at_ms
		FROM pending_pairings WHERE request_id=?`, requestID).Scan(
		&p.RequestID, &p.DeviceID, &p.PublicKey, &p.DisplayName, &p.Platform, &p.DeviceFamily,
		&p.ClientID, &p.ClientMode, &p.Role, &scopes, &p.RemoteIP, &p.CreatedAtMs)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrPendingNotFound
	}
	if err != nil {
		return nil, err
	}
	p.Scopes = unmarshalStrings(scopes)
	return &p, nil
}

// Reject deletes a pending pairing request.
func (s *Store) Reject(ctx context.Context, requestID string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM pending_pairings WHERE request_id=?`, requestID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrPendingNotFound
	}
	return nil
}

// Approve promotes a pending request to a paired device, minting one token per
// granted role. roles/scopes default to the pending request's values when empty.
func (s *Store) Approve(ctx context.Context, requestID string, roles, scopes []string) (*PairedDevice, []DeviceToken, error) {
	pending, err := s.getPending(ctx, requestID)
	if err != nil {
		return nil, nil, err
	}
	if len(roles) == 0 {
		if pending.Role != "" {
			roles = []string{pending.Role}
		} else {
			roles = []string{"node"}
		}
	}
	if scopes == nil {
		scopes = pending.Scopes
	}
	now := nowMs()
	dev := &PairedDevice{
		DeviceID: pending.DeviceID, PublicKey: pending.PublicKey, DisplayName: pending.DisplayName,
		Platform: pending.Platform, DeviceFamily: pending.DeviceFamily, ClientID: pending.ClientID,
		ClientMode: pending.ClientMode, Roles: roles, Scopes: scopes, CreatedAtMs: now, ApprovedAtMs: now,
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `INSERT INTO paired_devices
		(device_id, public_key, display_name, platform, device_family, client_id, client_mode, roles, scopes, created_at_ms, approved_at_ms, last_seen_at_ms)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,NULL)
		ON CONFLICT(device_id) DO UPDATE SET
		  public_key=excluded.public_key, display_name=excluded.display_name, platform=excluded.platform,
		  device_family=excluded.device_family, client_id=excluded.client_id, client_mode=excluded.client_mode,
		  roles=excluded.roles, scopes=excluded.scopes, approved_at_ms=excluded.approved_at_ms`,
		dev.DeviceID, dev.PublicKey, dev.DisplayName, dev.Platform, dev.DeviceFamily, dev.ClientID,
		dev.ClientMode, marshalStrings(dev.Roles), marshalStrings(dev.Scopes), dev.CreatedAtMs, dev.ApprovedAtMs); err != nil {
		return nil, nil, err
	}

	var tokens []DeviceToken
	for _, role := range roles {
		tok, err := randomHex(32)
		if err != nil {
			return nil, nil, err
		}
		dt := DeviceToken{Token: tok, DeviceID: dev.DeviceID, Role: role, Scopes: scopes, CreatedAtMs: now}
		if _, err := tx.ExecContext(ctx, `INSERT INTO device_tokens
			(token, device_id, role, scopes, created_at_ms, revoked_at_ms)
			VALUES (?,?,?,?,?,NULL)`,
			dt.Token, dt.DeviceID, dt.Role, marshalStrings(dt.Scopes), dt.CreatedAtMs); err != nil {
			return nil, nil, err
		}
		tokens = append(tokens, dt)
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM pending_pairings WHERE request_id=?`, requestID); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	return dev, tokens, nil
}

// GetPaired returns the paired device by id, or (nil,false) if not paired.
func (s *Store) GetPaired(ctx context.Context, deviceID string) (*PairedDevice, bool, error) {
	var d PairedDevice
	var roles, scopes string
	var lastSeen sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT device_id, public_key, display_name, platform, device_family,
		client_id, client_mode, roles, scopes, agent_id, created_at_ms, approved_at_ms, last_seen_at_ms
		FROM paired_devices WHERE device_id=?`, deviceID).Scan(
		&d.DeviceID, &d.PublicKey, &d.DisplayName, &d.Platform, &d.DeviceFamily, &d.ClientID,
		&d.ClientMode, &roles, &scopes, &d.AgentID, &d.CreatedAtMs, &d.ApprovedAtMs, &lastSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	d.Roles = unmarshalStrings(roles)
	d.Scopes = unmarshalStrings(scopes)
	d.LastSeenAtMs = lastSeen.Int64
	return &d, true, nil
}

// ListPaired returns all paired devices, newest-approved first.
func (s *Store) ListPaired(ctx context.Context) ([]PairedDevice, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT device_id, public_key, display_name, platform, device_family,
		client_id, client_mode, roles, scopes, agent_id, created_at_ms, approved_at_ms, last_seen_at_ms
		FROM paired_devices ORDER BY approved_at_ms DESC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []PairedDevice
	for rows.Next() {
		var d PairedDevice
		var roles, scopes string
		var lastSeen sql.NullInt64
		if err := rows.Scan(&d.DeviceID, &d.PublicKey, &d.DisplayName, &d.Platform, &d.DeviceFamily,
			&d.ClientID, &d.ClientMode, &roles, &scopes, &d.AgentID, &d.CreatedAtMs, &d.ApprovedAtMs, &lastSeen); err != nil {
			return nil, err
		}
		d.Roles = unmarshalStrings(roles)
		d.Scopes = unmarshalStrings(scopes)
		d.LastSeenAtMs = lastSeen.Int64
		out = append(out, d)
	}
	return out, rows.Err()
}

// SetDeviceAgent assigns the agent a paired device's turns route to ("" = gateway
// default). Returns ErrPairedNotFound if the device isn't paired.
func (s *Store) SetDeviceAgent(ctx context.Context, deviceID, agentID string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE paired_devices SET agent_id=? WHERE device_id=?`, agentID, deviceID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrPairedNotFound
	}
	return nil
}

// TokenByValue returns a non-revoked device token by its value, or (nil,false).
func (s *Store) TokenByValue(ctx context.Context, token string) (*DeviceToken, bool, error) {
	var t DeviceToken
	var scopes string
	var revoked sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT token, device_id, role, scopes, created_at_ms, revoked_at_ms
		FROM device_tokens WHERE token=?`, token).Scan(
		&t.Token, &t.DeviceID, &t.Role, &scopes, &t.CreatedAtMs, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if revoked.Valid && revoked.Int64 > 0 {
		return nil, false, nil
	}
	t.Scopes = unmarshalStrings(scopes)
	t.RevokedAtMs = revoked.Int64
	return &t, true, nil
}

// ListTokens returns the device's non-revoked tokens (for hello-ok issuance).
func (s *Store) ListTokens(ctx context.Context, deviceID string) ([]DeviceToken, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT token, device_id, role, scopes, created_at_ms, revoked_at_ms
		FROM device_tokens WHERE device_id=? AND (revoked_at_ms IS NULL OR revoked_at_ms=0)
		ORDER BY created_at_ms ASC`, deviceID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []DeviceToken
	for rows.Next() {
		var t DeviceToken
		var scopes string
		var revoked sql.NullInt64
		if err := rows.Scan(&t.Token, &t.DeviceID, &t.Role, &scopes, &t.CreatedAtMs, &revoked); err != nil {
			return nil, err
		}
		t.Scopes = unmarshalStrings(scopes)
		t.RevokedAtMs = revoked.Int64
		out = append(out, t)
	}
	return out, rows.Err()
}

// UpdateLastSeen stamps the device's last-seen time.
func (s *Store) UpdateLastSeen(ctx context.Context, deviceID string, ms int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE paired_devices SET last_seen_at_ms=? WHERE device_id=?`, ms, deviceID)
	return err
}

// RemovePaired deletes a paired device and revokes all its tokens.
func (s *Store) RemovePaired(ctx context.Context, deviceID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM device_tokens WHERE device_id=?`, deviceID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM paired_devices WHERE device_id=?`, deviceID); err != nil {
		return err
	}
	return tx.Commit()
}
