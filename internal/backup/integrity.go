// ClawEh
// License: MIT

package backup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, the one every store in the repo uses

	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/utils"
)

// dbBusyTimeout is how long a read waits on a writer's lock before giving up.
// A store mid-checkpoint holds the lock for milliseconds, so this only matters
// for a wedged process, and then failing is the right answer.
const dbBusyTimeout = "busy_timeout(5000)"

// openReadOnly opens a SQLite database read-only. Nothing this package does may
// change a live store, so every open goes through here. A WAL database whose
// owner is running is fine: readers never block the writer.
func openReadOnly(path string) (*sql.DB, error) {
	q := url.Values{}
	q.Set("mode", "ro")
	q.Add("_pragma", dbBusyTimeout)
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	return db, nil
}

// QuickCheck opens the SQLite database at path read-only and runs
// PRAGMA quick_check. It returns nil when SQLite answers "ok" and otherwise an
// error carrying what SQLite reported (or the open error for a file that is not
// a database). The backup runs it on every store before copying it, and the
// restore runs it on every restored file before the swap; store constructors
// can run it at open to refuse a corrupt file early.
func QuickCheck(path string) error {
	db, err := openReadOnly(path)
	if err != nil {
		return err
	}
	defer utils.CloseQuietly(db)
	rows, err := db.QueryContext(context.Background(), `PRAGMA quick_check`)
	if err != nil {
		return fmt.Errorf("quick_check %s: %w", path, err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			logger.DebugCF("backup", "rows close failed", map[string]any{"error": closeErr.Error()})
		}
	}()
	var problems []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return fmt.Errorf("quick_check %s: %w", path, err)
		}
		if line != "ok" {
			problems = append(problems, line)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("quick_check %s: %w", path, err)
	}
	if len(problems) > 0 {
		return fmt.Errorf("quick_check %s: %w", path, errors.New(strings.Join(problems, "; ")))
	}
	return nil
}

// vacuumInto writes a consistent, self-contained copy of the database at src
// to dst using SQLite's own VACUUM INTO on a read-only connection. Unlike a
// file copy it takes a read transaction, so committed rows still sitting in the
// WAL are included and a writer mid-transaction cannot leave the copy torn. dst
// must not exist (SQLite refuses to overwrite).
func vacuumInto(src, dst string) error {
	db, err := openReadOnly(src)
	if err != nil {
		return err
	}
	defer utils.CloseQuietly(db)
	if _, err := db.ExecContext(context.Background(), `VACUUM INTO ?`, dst); err != nil {
		return fmt.Errorf("vacuum %s into %s: %w", src, dst, err)
	}
	return nil
}
