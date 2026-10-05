// ClawEh
// License: MIT

package fusion

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"github.com/PivotLLM/toolspec"
	_ "modernc.org/sqlite"

	"github.com/PivotLLM/ClawEh/internal/perms"
	"github.com/PivotLLM/ClawEh/utils"
)

// sqliteDataStore implements toolspec.DataStore over a single shared SQLite file.
// Fusion's auth adapter (datastore_tokenstore.go) partitions its records by
// collection (oauth, creds, authcodes, index), so one (collection,key) keyspace
// backs every tenant's OAuth tokens and auth codes. *sql.DB is concurrency-safe,
// so this store is shared process-wide across agents.
type sqliteDataStore struct {
	db *sql.DB
}

// connectionPragmas is the DSN query every pooled connection is opened with.
const connectionPragmas = "_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)"

// NewSQLiteDataStore opens (or creates) the fusion token store at path with WAL
// mode and returns it as a toolspec.DataStore. Pure Go (modernc.org/sqlite), no
// CGO. The parent directory is created (0700) since it holds OAuth secrets.
func NewSQLiteDataStore(path string) (toolspec.DataStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("fusion datastore: create dir: %w", err)
	}
	// OAuth tokens live in here: make the file private before SQLite creates
	// it, so the -wal/-shm side files inherit that mode too.
	if err := perms.EnsurePrivateFile(path); err != nil {
		return nil, fmt.Errorf("fusion datastore: %w", err)
	}
	// The pragmas travel in the DSN so the driver applies them to EVERY
	// connection database/sql opens, not only the first: an Exec'd PRAGMA
	// reaches one pooled connection, and a later one opened under load ran
	// with busy_timeout=0, failing a contended write at once with SQLITE_BUSY.
	// journal_mode is safe here because this store has a single opener
	// (sharedEngine's sync.Once), so the one-time WAL conversion of a fresh
	// file cannot race another writer.
	db, err := sql.Open("sqlite", path+"?"+connectionPragmas)
	if err != nil {
		return nil, fmt.Errorf("fusion datastore: open %s: %w", path, err)
	}
	if _, err := db.ExecContext(context.Background(),
		`CREATE TABLE IF NOT EXISTS kv (
			collection TEXT NOT NULL,
			key        TEXT NOT NULL,
			value      BLOB NOT NULL,
			PRIMARY KEY (collection, key)
		)`); err != nil {
		utils.CloseQuietly(db)
		return nil, fmt.Errorf("fusion datastore: schema: %w", err)
	}
	return &sqliteDataStore{db: db}, nil
}

// Get returns the value stored under (collection, key). ok is false (with no
// error) when no such record exists; err is reserved for real backend failures.
func (s *sqliteDataStore) Get(ctx context.Context, collection, key string) ([]byte, bool, error) {
	var value []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT value FROM kv WHERE collection=? AND key=?`, collection, key).Scan(&value)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("fusion datastore: get: %w", err)
	}
	return value, true, nil
}

// Set writes value under (collection, key), overwriting any prior value.
func (s *sqliteDataStore) Set(ctx context.Context, collection, key string, value []byte) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO kv (collection, key, value) VALUES (?, ?, ?)
			ON CONFLICT(collection, key) DO UPDATE SET value=excluded.value`,
		collection, key, value)
	if err != nil {
		return fmt.Errorf("fusion datastore: set: %w", err)
	}
	return nil
}

// Delete removes (collection, key). Deleting an absent record is not an error.
func (s *sqliteDataStore) Delete(ctx context.Context, collection, key string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM kv WHERE collection=? AND key=?`, collection, key)
	if err != nil {
		return fmt.Errorf("fusion datastore: delete: %w", err)
	}
	return nil
}
