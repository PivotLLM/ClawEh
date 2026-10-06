// ClawEh - Cognitive Memory
// License: MIT

package cogmemhost

import (
	"github.com/PivotLLM/cogmem/store"

	"github.com/PivotLLM/ClawEh/internal/perms"
)

// EnsurePrivate makes the memory directory dir and its database owner-only
// before the cogmem module opens them: the directory is created (or tightened
// to) 0700 and the database created (or tightened to) 0600, so SQLite gives
// its -wal and -shm side files the same mode from the first write. The module
// itself would create both with the umask default. An empty dir is a no-op.
func EnsurePrivate(dir string) error {
	if dir == "" {
		return nil
	}
	if err := perms.EnsurePrivateDir(dir); err != nil {
		return err
	}
	return perms.EnsurePrivateFile(store.DBPath(dir))
}
