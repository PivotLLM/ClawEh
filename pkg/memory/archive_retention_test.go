package memory

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// hasConsolidatedColumn reports whether the messages table carries the
// consolidated column, inspected via PRAGMA table_info.
func hasConsolidatedColumn(t *testing.T, db *sql.DB) bool {
	t.Helper()
	rows, err := db.Query(`PRAGMA table_info(messages)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid     int
			name    string
			ctype   string
			notNull int
			dflt    sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if name == "consolidated" {
			return true
		}
	}
	return false
}

// TestMigrateConsolidatedColumn verifies Open adds the consolidated column to a
// pre-existing archive that lacks it, without disturbing existing rows.
func TestMigrateConsolidatedColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.archive.db")

	// Build a legacy DB with the original messages schema (no consolidated col).
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	const legacySchema = `
CREATE TABLE messages (
    seq        INTEGER PRIMARY KEY,
    role       TEXT    NOT NULL,
    payload    TEXT    NOT NULL,
    text       TEXT    NOT NULL,
    created_at INTEGER NOT NULL
);`
	if _, err := db.Exec(legacySchema); err != nil {
		t.Fatalf("legacy schema: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO messages (seq, role, payload, text, created_at) VALUES (1,'user','{}','hello',?)`,
		time.Now().Unix(),
	); err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}
	if hasConsolidatedColumn(t, db) {
		t.Fatal("legacy DB unexpectedly already had consolidated column")
	}
	_ = db.Close()

	// Open via the production path: should migrate.
	a, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer a.Close()

	if !hasConsolidatedColumn(t, a.db) {
		t.Fatal("Open did not add consolidated column to legacy DB")
	}
	// Existing row must survive the migration with consolidated defaulting to 0.
	if got := countMessages(t, a); got != 1 {
		t.Fatalf("count = %d, want 1", got)
	}

	// Re-open: migration must be idempotent (no error, column still present).
	if err := a.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	a2, err := Open(path)
	if err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	defer a2.Close()
	if !hasConsolidatedColumn(t, a2.db) {
		t.Fatal("consolidated column missing after re-open")
	}
}

// consolidatedFlag returns the consolidated value for the given seq.
func consolidatedFlag(t *testing.T, a *ArchiveStore, seq int64) int {
	t.Helper()
	var v int
	if err := a.db.QueryRow(`SELECT consolidated FROM messages WHERE seq=?`, seq).Scan(&v); err != nil {
		t.Fatalf("scan consolidated seq=%d: %v", seq, err)
	}
	return v
}

// TestMarkConsolidated verifies the flag is set for seq <= uptoSeq only.
func TestMarkConsolidated(t *testing.T) {
	a := openTestArchive(t)
	now := time.Now()
	for i := 1; i <= 5; i++ {
		if err := a.Append(int64(i), sampleMsg("user", "m"), now); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}

	if err := a.MarkConsolidated(3); err != nil {
		t.Fatalf("MarkConsolidated: %v", err)
	}

	for seq := int64(1); seq <= 3; seq++ {
		if got := consolidatedFlag(t, a, seq); got != 1 {
			t.Fatalf("seq %d consolidated = %d, want 1", seq, got)
		}
	}
	for seq := int64(4); seq <= 5; seq++ {
		if got := consolidatedFlag(t, a, seq); got != 0 {
			t.Fatalf("seq %d consolidated = %d, want 0", seq, got)
		}
	}
}

// TestMarkConsolidated_NoOpAtZero verifies uptoSeq <= 0 flags nothing.
func TestMarkConsolidated_NoOpAtZero(t *testing.T) {
	a := openTestArchive(t)
	now := time.Now()
	for i := 1; i <= 3; i++ {
		if err := a.Append(int64(i), sampleMsg("user", "m"), now); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	if err := a.MarkConsolidated(0); err != nil {
		t.Fatalf("MarkConsolidated(0): %v", err)
	}
	for seq := int64(1); seq <= 3; seq++ {
		if got := consolidatedFlag(t, a, seq); got != 0 {
			t.Fatalf("seq %d consolidated = %d, want 0", seq, got)
		}
	}
}

// TestPruneMessagesToCount_ProtectionKeepsUnconsolidated verifies that with the
// retention guard ON, unconsolidated rows are kept even beyond the newest-N
// window, while consolidated rows beyond the window are pruned.
func TestPruneMessagesToCount_ProtectionKeepsUnconsolidated(t *testing.T) {
	a := openTestArchive(t)
	a.SetProtectUnconsolidated(true)
	now := time.Now()
	for i := 1; i <= 10; i++ {
		if err := a.Append(int64(i), sampleMsg("user", "m"), now); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	// Only seqs 1..4 are consolidated; 5..10 are not.
	if err := a.MarkConsolidated(4); err != nil {
		t.Fatalf("MarkConsolidated: %v", err)
	}

	// Keep newest 3 (8,9,10). Without protection seqs 1..7 would be deleted; with
	// protection seqs 5,6,7 (unconsolidated) must survive, only 1..4 are pruned.
	if err := a.PruneMessagesToCount(3); err != nil {
		t.Fatalf("PruneMessagesToCount: %v", err)
	}

	min, max, err := a.Bounds()
	if err != nil {
		t.Fatalf("Bounds: %v", err)
	}
	if min != 5 || max != 10 {
		t.Fatalf("bounds = [%d,%d], want [5,10]", min, max)
	}
	if got := countMessages(t, a); got != 6 {
		t.Fatalf("count = %d, want 6 (seqs 5..10)", got)
	}
}

// TestPruneMessagesToCount_ProtectionOffUnchanged verifies that with the guard
// OFF the count-based prune behaves exactly as before, deleting unconsolidated
// rows beyond the window.
func TestPruneMessagesToCount_ProtectionOffUnchanged(t *testing.T) {
	a := openTestArchive(t)
	// protection is OFF by default.
	now := time.Now()
	for i := 1; i <= 10; i++ {
		if err := a.Append(int64(i), sampleMsg("user", "m"), now); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	// Mark some consolidated to prove it is ignored when protection is off.
	if err := a.MarkConsolidated(4); err != nil {
		t.Fatalf("MarkConsolidated: %v", err)
	}

	if err := a.PruneMessagesToCount(3); err != nil {
		t.Fatalf("PruneMessagesToCount: %v", err)
	}
	min, max, err := a.Bounds()
	if err != nil {
		t.Fatalf("Bounds: %v", err)
	}
	if min != 8 || max != 10 {
		t.Fatalf("bounds = [%d,%d], want [8,10]", min, max)
	}
	if got := countMessages(t, a); got != 3 {
		t.Fatalf("count = %d, want 3", got)
	}
}

// TestPruneMessagesBefore_ProtectionKeepsUnconsolidated verifies that with the
// guard ON, only consolidated rows older than the cutoff are deleted.
func TestPruneMessagesBefore_ProtectionKeepsUnconsolidated(t *testing.T) {
	a := openTestArchive(t)
	a.SetProtectUnconsolidated(true)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// seqs 1..5 at day 1..5.
	for i := 1; i <= 5; i++ {
		ts := base.AddDate(0, 0, i-1)
		if err := a.Append(int64(i), sampleMsg("user", "m"), ts); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	// Only seq 1 (day 1) is consolidated; seq 2 (day 2) is not.
	if err := a.MarkConsolidated(1); err != nil {
		t.Fatalf("MarkConsolidated: %v", err)
	}

	// Cutoff at day 3: rows at day1,day2 are older. Only the consolidated day-1
	// row (seq 1) may be deleted; the unconsolidated day-2 row (seq 2) survives.
	cutoff := base.AddDate(0, 0, 2)
	if err := a.PruneMessagesBefore(cutoff); err != nil {
		t.Fatalf("PruneMessagesBefore: %v", err)
	}

	min, max, err := a.Bounds()
	if err != nil {
		t.Fatalf("Bounds: %v", err)
	}
	if min != 2 || max != 5 {
		t.Fatalf("bounds = [%d,%d], want [2,5]", min, max)
	}
}

// TestPruneMessagesBefore_ProtectionOffUnchanged verifies that with the guard
// OFF the date-based prune deletes all rows older than the cutoff regardless of
// the consolidated flag.
func TestPruneMessagesBefore_ProtectionOffUnchanged(t *testing.T) {
	a := openTestArchive(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 1; i <= 5; i++ {
		ts := base.AddDate(0, 0, i-1)
		if err := a.Append(int64(i), sampleMsg("user", "m"), ts); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	if err := a.MarkConsolidated(1); err != nil {
		t.Fatalf("MarkConsolidated: %v", err)
	}

	cutoff := base.AddDate(0, 0, 2)
	if err := a.PruneMessagesBefore(cutoff); err != nil {
		t.Fatalf("PruneMessagesBefore: %v", err)
	}
	min, max, err := a.Bounds()
	if err != nil {
		t.Fatalf("Bounds: %v", err)
	}
	// Both seq 1 and seq 2 deleted (days 1,2 < cutoff), regardless of flag.
	if min != 3 || max != 5 {
		t.Fatalf("bounds = [%d,%d], want [3,5]", min, max)
	}
}
