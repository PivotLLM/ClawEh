// ClawEh
// License: MIT

package fusion

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"

	"github.com/PivotLLM/toolspec"
)

// sqliteDataStore implements toolspec.DataStore over a single shared SQLite file.
// Fusion's auth adapter (datastore_tokenstore.go) partitions its records by
// collection (oauth, creds, authcodes, index), so one (collection,key) keyspace
// backs every tenant's OAuth tokens and auth codes. *sql.DB is concurrency-safe,
// so this store is shared process-wide across agents.
type sqliteDataStore struct {
	db *sql.DB
}

// NewSQLiteDataStore opens (or creates) the fusion token store at path with WAL
// mode and returns it as a toolspec.DataStore. Pure Go (modernc.org/sqlite), no
// CGO. The parent directory is created (0700) since it holds OAuth secrets.
func NewSQLiteDataStore(path string) (toolspec.DataStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("fusion datastore: create dir: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("fusion datastore: open %s: %w", path, err)
	}
	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA synchronous=NORMAL",
	}
	for _, p := range pragmas {
		if _, err := db.ExecContext(context.Background(), p); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("fusion datastore: %q: %w", p, err)
		}
	}
	if _, err := db.ExecContext(context.Background(),
		`CREATE TABLE IF NOT EXISTS kv (
			collection TEXT NOT NULL,
			key        TEXT NOT NULL,
			value      BLOB NOT NULL,
			PRIMARY KEY (collection, key)
		)`); err != nil {
		_ = db.Close()
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
