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

	"github.com/PivotLLM/ClawEh/internal/perms"
	"github.com/PivotLLM/ClawEh/internal/tokenhash"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/utils"
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

// DeviceToken is a persistent bearer token issued per granted role. The
// database holds only tokenhash.Hash(token); Token carries the plaintext when
// a token is minted (Approve, IssueTokens) or when it was presented for lookup
// (TokenByValue), which are the only times the plaintext exists.
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
// Converting a fresh database to WAL can lose a race with another connection
// doing the same thing. The window is one connection's schema setup, so a short
// bounded retry covers it without delaying a genuinely locked database.
const (
	walConvertAttempts = 5
	walConvertBackoff  = 40 * time.Millisecond
)

// connectionPragmas are per-connection settings. They travel in the DSN so the
// driver applies them to EVERY connection database/sql opens, not only the
// first: an Exec'd PRAGMA reaches one pooled connection, and a later one
// opened under load ran with busy_timeout=0, failing a contended write at
// once with SQLITE_BUSY. The driver applies busy_timeout before the rest.
//
// journal_mode is deliberately NOT here. It is a property of the file, not the
// connection, so it needs setting once — and converting a fresh file to WAL is
// the racy step ensureWAL exists for. Re-running it on every pooled connection
// would put that race back on every open.
const connectionPragmas = "_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)"

func OpenStore(ctx context.Context, path string) (*Store, error) {
	// Pairing tokens live in here: make the file private before SQLite
	// creates it, so the -wal/-shm side files inherit that mode too.
	if err := perms.EnsurePrivateFile(path); err != nil {
		return nil, fmt.Errorf("device: %w", err)
	}
	db, err := sql.Open("sqlite", path+"?"+connectionPragmas)
	if err != nil {
		return nil, fmt.Errorf("device: open %s: %w", path, err)
	}
	if err := ensureWAL(ctx, db); err != nil {
		utils.CloseQuietly(db)
		return nil, err
	}
	if _, err := db.ExecContext(ctx, deviceSchema); err != nil {
		utils.CloseQuietly(db)
		return nil, fmt.Errorf("device: schema: %w", err)
	}
	// Migration for DBs created before agent_id existed. ADD COLUMN fails with a
	// "duplicate column" error once applied, which is the steady state — ignore it.
	if _, err := db.ExecContext(ctx,
		`ALTER TABLE paired_devices ADD COLUMN agent_id TEXT NOT NULL DEFAULT ''`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column") {
		utils.CloseQuietly(db)
		return nil, fmt.Errorf("device: migrate agent_id: %w", err)
	}
	if err := hashPlaintextTokens(ctx, db); err != nil {
		utils.CloseQuietly(db)
		return nil, fmt.Errorf("device: migrate tokens: %w", err)
	}
	return &Store{db: db}, nil
}

// hashPlaintextTokens is the one-time migration for databases written before
// device tokens were hashed at rest: every device_tokens.token that is not yet
// in tokenhash form is replaced by its hash. The plaintext is in the row, so
// the hash can be computed without the device's help, and the device's own
// copy keeps authenticating because TokenByValue hashes what it is presented.
// Idempotent: on an already-migrated database it matches no rows.
func hashPlaintextTokens(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `SELECT token FROM device_tokens WHERE token NOT LIKE ?`, tokenhash.Prefix+"%")
	if err != nil {
		return err
	}
	var plain []string
	for rows.Next() {
		var tok string
		if err = rows.Scan(&tok); err != nil {
			utils.CloseQuietly(rows)
			return err
		}
		plain = append(plain, tok)
	}
	if err = rows.Err(); err != nil {
		utils.CloseQuietly(rows)
		return err
	}
	utils.CloseQuietly(rows)
	if len(plain) == 0 {
		return nil
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	for _, tok := range plain {
		if _, err := tx.ExecContext(ctx, `UPDATE device_tokens SET token=? WHERE token=?`, tokenhash.Hash(tok), tok); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	logger.InfoCF("device", "hashed stored device tokens", map[string]any{"count": len(plain)})
	return nil
}

// Close closes the database.
// ensureWAL puts the database in WAL mode, tolerating a concurrent converter.
//
// This is deliberately not a plain `PRAGMA journal_mode=WAL` in the list above,
// which is what it used to be and what caused an intermittent 500 on the WebUI
// devices page — roughly one gateway restart in three:
//
//	device: "PRAGMA journal_mode=WAL": database is locked (5) (SQLITE_BUSY)
//
// Two things make that pragma different from the others:
//
//  1. CONVERTING a database to WAL needs an exclusive lock, so it loses to any
//     connection holding a write lock — the device channel opening the same file
//     at startup, racing this admin API.
//  2. busy_timeout does NOT cover it. SQLite returns SQLITE_BUSY immediately
//     rather than invoking the busy handler, so no amount of reordering or
//     waiting configuration helps. Measured: it fails in under 200µs with a 5 s
//     busy_timeout in force.
//
// The mode is a property of the FILE, not of the connection, and it persists.
// So: read it, and only convert when it is not already WAL — which is a fresh
// database, and only there can two openers genuinely meet. In that case the
// loser retries briefly, by which point the winner has converted the file and
// the read confirms it. If it still cannot be established, the open proceeds
// anyway: a rollback-journal database is slower under concurrency but entirely
// correct, and failing the open outright is what produced the 500.
func ensureWAL(ctx context.Context, db *sql.DB) error {
	current := func() (string, error) {
		var mode string
		err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode)
		return strings.ToLower(mode), err
	}

	mode, err := current()
	if err != nil {
		return fmt.Errorf("device: read journal_mode: %w", err)
	}
	if mode == "wal" {
		return nil
	}

	for range walConvertAttempts {
		if _, err := db.ExecContext(ctx, "PRAGMA journal_mode=WAL"); err == nil {
			return nil
		}
		// Someone else may have converted it while this attempt was refused.
		if mode, err := current(); err == nil && mode == "wal" {
			return nil
		}
		time.Sleep(walConvertBackoff)
	}

	logger.WarnCF("device", "could not switch pairing DB to WAL; continuing on the rollback journal",
		map[string]any{"attempts": walConvertAttempts})
	return nil
}

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
	b, err := json.Marshal(ss)
	if err != nil {
		return "" // unreachable: a []string always marshals
	}
	return string(b)
}

// rollback discards tx after a failed transaction; ErrTxDone is the normal
// outcome when Commit already ran and is not worth a log line.
func rollback(tx *sql.Tx) {
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		logger.WarnCF("device", "transaction rollback failed", map[string]any{"error": err.Error()})
	}
}

func unmarshalStrings(s string) []string {
	if s == "" {
		return nil
	}
	var ss []string
	if err := json.Unmarshal([]byte(s), &ss); err != nil {
		logger.WarnCF("device", "stored string list is not valid JSON", map[string]any{"error": err.Error()})
	}
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
	defer rollback(tx)

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

	if _, execErr := tx.ExecContext(ctx, `DELETE FROM pending_pairings WHERE device_id=?`, p.DeviceID); execErr != nil {
		return "", execErr
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
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			logger.DebugCF("device", "rows close failed", map[string]any{"error": closeErr.Error()})
		}
	}()
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
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
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
	defer rollback(tx)

	if _, err = tx.ExecContext(ctx, `INSERT INTO paired_devices
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

	tokens, err := insertTokens(ctx, tx, dev.DeviceID, roles, scopes, now)
	if err != nil {
		return nil, nil, err
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
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			logger.DebugCF("device", "rows close failed", map[string]any{"error": closeErr.Error()})
		}
	}()
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
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrPairedNotFound
	}
	return nil
}

// insertTokens mints one token per role for deviceID inside tx, storing each
// token's hash, and returns the plaintext tokens for the caller to hand out.
func insertTokens(ctx context.Context, tx *sql.Tx, deviceID string, roles, scopes []string, now int64) ([]DeviceToken, error) {
	var tokens []DeviceToken
	for _, role := range roles {
		tok, err := randomHex(32)
		if err != nil {
			return nil, err
		}
		dt := DeviceToken{Token: tok, DeviceID: deviceID, Role: role, Scopes: scopes, CreatedAtMs: now}
		if _, err := tx.ExecContext(ctx, `INSERT INTO device_tokens
			(token, device_id, role, scopes, created_at_ms, revoked_at_ms)
			VALUES (?,?,?,?,?,NULL)`,
			tokenhash.Hash(dt.Token), dt.DeviceID, dt.Role, marshalStrings(dt.Scopes), dt.CreatedAtMs); err != nil {
			return nil, err
		}
		tokens = append(tokens, dt)
	}
	return tokens, nil
}

// TokenByValue returns the non-revoked device token a client presented, or
// (nil,false). The presented value is hashed for the lookup; the returned
// Token is the presented plaintext.
func (s *Store) TokenByValue(ctx context.Context, token string) (*DeviceToken, bool, error) {
	var t DeviceToken
	var scopes string
	var revoked sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT device_id, role, scopes, created_at_ms, revoked_at_ms
		FROM device_tokens WHERE token=?`, tokenhash.Hash(token)).Scan(
		&t.DeviceID, &t.Role, &scopes, &t.CreatedAtMs, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if revoked.Valid && revoked.Int64 > 0 {
		return nil, false, nil
	}
	t.Token = token
	t.Scopes = unmarshalStrings(scopes)
	t.RevokedAtMs = revoked.Int64
	return &t, true, nil
}

// IssueTokens replaces every token the device holds with a fresh one per role
// and returns the new plaintext tokens. It is how hello-ok hands a device its
// tokens when the device did not present one (first connect after approval,
// or a reconnect on the shared secret): the stored hashes cannot be sent back,
// so the device gets new tokens and the ones it did not use stop working.
func (s *Store) IssueTokens(ctx context.Context, deviceID string, roles, scopes []string) ([]DeviceToken, error) {
	if len(roles) == 0 {
		roles = []string{"node"}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	if _, err = tx.ExecContext(ctx, `DELETE FROM device_tokens WHERE device_id=?`, deviceID); err != nil {
		return nil, err
	}
	tokens, err := insertTokens(ctx, tx, deviceID, roles, scopes, nowMs())
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return tokens, nil
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
	defer rollback(tx)
	if _, err := tx.ExecContext(ctx, `DELETE FROM device_tokens WHERE device_id=?`, deviceID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM paired_devices WHERE device_id=?`, deviceID); err != nil {
		return err
	}
	return tx.Commit()
}
