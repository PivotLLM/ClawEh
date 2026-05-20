package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"os"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/PivotLLM/ClawEh/pkg/providers"
)

// ErrArchiveUnavailable is returned when the ArchiveStore failed to open and
// all subsequent operations are no-ops or empty returns. Exported so callers
// can distinguish graceful-degradation failures from real errors.
var ErrArchiveUnavailable = errors.New("memory: archive unavailable")

const archiveSchema = `
CREATE TABLE IF NOT EXISTS messages (
    seq        INTEGER PRIMARY KEY,
    role       TEXT    NOT NULL,
    payload    TEXT    NOT NULL,
    text       TEXT    NOT NULL,
    created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_created_at ON messages(created_at);
CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
    text,
    content='messages',
    content_rowid='seq'
);
CREATE TRIGGER IF NOT EXISTS messages_ai AFTER INSERT ON messages BEGIN
    INSERT INTO messages_fts(rowid, text) VALUES (new.seq, new.text);
END;
`

// SearchResult holds one result from an ArchiveStore.Search call.
// It pairs the archive sequence number with the deserialized message.
type SearchResult struct {
	Seq     int
	Message providers.Message
}

// ArchiveStore is an append-only SQLite store for session message archives.
// It supports FTS5 full-text search and efficient seq-range retrieval.
//
// The caller (ContextManager) is the sole writer. Readers open separate
// read-only connections using the file:path?mode=ro DSN; WAL mode allows
// concurrent readers without blocking the writer.
type ArchiveStore struct {
	db          *sql.DB
	path        string
	mu          sync.Mutex // held only for writes (Append, Delete)
	unavailable bool       // set true when Open fails; all ops are no-ops
}

// OpenReadOnly creates a read-only ArchiveStore for an existing archive at path.
// Only QueryRange, Search, and Bounds are valid on the returned store; each
// opens its own short-lived read-only SQLite connection. No write connection,
// WAL pragma, or schema creation is performed.
// Returns ErrArchiveUnavailable if the file does not exist or is unreadable.
func OpenReadOnly(path string) (*ArchiveStore, error) {
	// Verify the file exists before returning the store.
	if _, err := os.Stat(path); err != nil {
		return &ArchiveStore{path: path, unavailable: true}, ErrArchiveUnavailable
	}
	return &ArchiveStore{path: path}, nil
}

// Open opens (or creates) an archive database at path.
// Enables WAL mode, creates schema, and runs an FTS5 integrity check.
// On failure it returns a store with unavailable=true plus ErrArchiveUnavailable.
func Open(path string) (*ArchiveStore, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		log.Printf("memory: archive open %q: %v", path, err)
		return &ArchiveStore{path: path, unavailable: true}, ErrArchiveUnavailable
	}

	// Single writer connection; no connection pooling needed.
	db.SetMaxOpenConns(1)

	// Enable WAL mode for concurrent reader support.
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		log.Printf("memory: archive WAL %q: %v", path, err)
		return &ArchiveStore{path: path, unavailable: true}, ErrArchiveUnavailable
	}
	if _, err := db.Exec("PRAGMA busy_timeout=2000"); err != nil {
		db.Close()
		log.Printf("memory: archive busy_timeout %q: %v", path, err)
		return &ArchiveStore{path: path, unavailable: true}, ErrArchiveUnavailable
	}

	// Create schema.
	if _, err := db.Exec(archiveSchema); err != nil {
		db.Close()
		log.Printf("memory: archive schema %q: %v", path, err)
		return &ArchiveStore{path: path, unavailable: true}, ErrArchiveUnavailable
	}

	// FTS5 integrity check; rebuild if needed.
	if _, err := db.Exec("INSERT INTO messages_fts(messages_fts) VALUES ('integrity-check')"); err != nil {
		log.Printf("memory: archive FTS5 integrity check failed for %q, rebuilding: %v", path, err)
		if _, rebuildErr := db.Exec("INSERT INTO messages_fts(messages_fts) VALUES ('rebuild')"); rebuildErr != nil {
			db.Close()
			log.Printf("memory: archive FTS5 rebuild failed %q: %v", path, rebuildErr)
			return &ArchiveStore{path: path, unavailable: true}, ErrArchiveUnavailable
		}
	}

	return &ArchiveStore{db: db, path: path}, nil
}

// Append writes one message to the archive.
// msg is serialized as JSON payload; searchable text is derived from
// msg.Content plus any ToolCalls arguments.
// Acquires the write mutex; no-op if unavailable.
func (a *ArchiveStore) Append(seq int, msg providers.Message, createdAt time.Time) error {
	if a.unavailable {
		return nil
	}

	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	text := deriveArchiveText(msg)

	a.mu.Lock()
	defer a.mu.Unlock()

	_, err = a.db.Exec(
		`INSERT OR IGNORE INTO messages (seq, role, payload, text, created_at) VALUES (?, ?, ?, ?, ?)`,
		seq, msg.Role, string(payload), text, createdAt.Unix(),
	)
	return err
}

// QueryRange returns messages with seq in [minSeq, maxSeq] inclusive.
// Uses a read-only connection opened and closed per call.
// Returns (nil, ErrArchiveUnavailable) if the store is unavailable.
func (a *ArchiveStore) QueryRange(minSeq, maxSeq int) ([]providers.Message, error) {
	if a.unavailable {
		return nil, ErrArchiveUnavailable
	}

	db, err := sql.Open("sqlite", "file:"+a.path+"?mode=ro")
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.QueryContext(context.Background(),
		`SELECT payload FROM messages WHERE seq BETWEEN ? AND ? ORDER BY seq`,
		minSeq, maxSeq,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanMessages(rows)
}

// Search runs an FTS5 full-text query and returns matching messages with their
// archive sequence numbers.
// query must not exceed 500 characters.
// role is an optional filter ("user"/"assistant"/"tool"); empty = all roles.
// limit is clamped to [1, 100].
// Uses a read-only connection opened and closed per call.
// Returns (nil, ErrArchiveUnavailable) if the store is unavailable.
// FTS5 parse errors are returned as regular errors (not panics); callers should
// surface them as tool errors.
func (a *ArchiveStore) Search(ctx context.Context, query, role string, limit int) ([]SearchResult, error) {
	if a.unavailable {
		return nil, ErrArchiveUnavailable
	}
	if len(query) > 500 {
		return nil, errors.New("memory: archive search query exceeds 500 characters")
	}
	if limit < 1 {
		limit = 1
	}
	if limit > 100 {
		limit = 100
	}

	db, err := sql.Open("sqlite", "file:"+a.path+"?mode=ro")
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx,
		`SELECT m.seq, m.payload
		 FROM   messages_fts
		 JOIN   messages m ON m.seq = messages_fts.rowid
		 WHERE  messages_fts MATCH ?
		   AND  (? = '' OR m.role = ?)
		 ORDER  BY rank
		 LIMIT  ?`,
		query, role, role, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanSearchResults(rows)
}

// Bounds returns the inclusive [minSeq, maxSeq] of stored messages.
// Returns (0, 0, nil) if no messages exist.
// Uses a read-only connection opened and closed per call.
func (a *ArchiveStore) Bounds() (minSeq, maxSeq int, err error) {
	if a.unavailable {
		return 0, 0, ErrArchiveUnavailable
	}

	db, err := sql.Open("sqlite", "file:"+a.path+"?mode=ro")
	if err != nil {
		return 0, 0, err
	}
	defer db.Close()

	row := db.QueryRowContext(context.Background(),
		`SELECT COALESCE(MIN(seq), 0), COALESCE(MAX(seq), 0) FROM messages`,
	)
	err = row.Scan(&minSeq, &maxSeq)
	return minSeq, maxSeq, err
}

// Close closes the write connection. Safe to call multiple times.
func (a *ArchiveStore) Close() error {
	if a.unavailable || a.db == nil {
		return nil
	}
	err := a.db.Close()
	a.db = nil
	return err
}

// Delete closes the connection and removes the .db file.
func (a *ArchiveStore) Delete() error {
	if err := a.Close(); err != nil {
		return err
	}
	if a.path == "" {
		return nil
	}
	err := os.Remove(a.path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// deriveArchiveText produces the FTS-indexed text for a message:
// msg.Content followed by any ToolCalls arguments serialized as JSON.
func deriveArchiveText(msg providers.Message) string {
	text := msg.Content
	if len(msg.ToolCalls) > 0 {
		if b, err := json.Marshal(msg.ToolCalls); err == nil {
			if text != "" {
				text += " "
			}
			text += string(b)
		}
	}
	return text
}

// scanMessages reads payload column rows and deserializes each into a providers.Message.
func scanMessages(rows *sql.Rows) ([]providers.Message, error) {
	var msgs []providers.Message
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var msg providers.Message
		if err := json.Unmarshal([]byte(payload), &msg); err != nil {
			return nil, err
		}
		msgs = append(msgs, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return msgs, nil
}

// scanSearchResults reads (seq, payload) rows and deserializes each into a SearchResult.
func scanSearchResults(rows *sql.Rows) ([]SearchResult, error) {
	var results []SearchResult
	for rows.Next() {
		var seq int
		var payload string
		if err := rows.Scan(&seq, &payload); err != nil {
			return nil, err
		}
		var msg providers.Message
		if err := json.Unmarshal([]byte(payload), &msg); err != nil {
			return nil, err
		}
		results = append(results, SearchResult{Seq: seq, Message: msg})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return results, nil
}
