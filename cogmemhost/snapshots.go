// ClawEh - Cognitive Memory
// License: MIT

package cogmemhost

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// SnapshotMaxAge is how long a pre-migration snapshot is kept before the
// nightly retention pass removes it. Long enough to notice a bad upgrade and
// roll back; short enough that a store migrated every release does not keep a
// full copy of itself per version forever.
const SnapshotMaxAge = 30 * 24 * time.Hour

// snapshotName matches the files store.snapshotBeforeMigration writes beside
// the live database: <name>.pre-v<N>.db.
var snapshotName = regexp.MustCompile(`\.pre-v\d+\.db$`)

// PruneSnapshots removes every pre-migration snapshot in dir (an agent's
// cogmem directory) whose modification time is older than maxAge as of now,
// and returns the paths removed. The live database, its WAL files and the
// subagents directory are never touched. A missing dir is not an error. The
// live database is not vacuumed: cogmem exposes no maintenance hook for the
// store the running gateway holds open, and a snapshot's removal frees its
// own space without one.
func PruneSnapshots(dir string, now time.Time, maxAge time.Duration) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cogmem: prune snapshots: %w", err)
	}
	var removed []string
	for _, entry := range entries {
		if entry.IsDir() || !snapshotName.MatchString(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return removed, fmt.Errorf("cogmem: prune snapshots: %w", err)
		}
		if now.Sub(info.ModTime()) < maxAge {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if err := os.Remove(path); err != nil {
			return removed, fmt.Errorf("cogmem: prune snapshots: %w", err)
		}
		removed = append(removed, path)
	}
	return removed, nil
}
