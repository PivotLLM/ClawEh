package files

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
)

// backupExistingFile copies path to a numbered sibling <basename>.NNNN before
// modification, where NNNN is one greater than the highest existing 4-digit
// suffix among siblings (starting at 0001). When the target does not exist,
// backup is silently skipped and the modification proceeds.
//
// Returns the backup path created, or "" when no backup was made.
func backupExistingFile(fsys fileSystem, path string) (string, error) {
	info, err := fsys.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("backup: failed to stat target: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("backup: target is a directory: %s", path)
	}

	dir := filepath.Dir(path)
	base := filepath.Base(path)

	entries, err := fsys.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("backup: failed to scan siblings: %w", err)
	}

	next, err := nextBackupSuffix(base, entries)
	if err != nil {
		return "", err
	}

	data, err := fsys.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("backup: failed to read target: %w", err)
	}

	const maxAttempts = 100
	for attempt := 0; attempt < maxAttempts; attempt++ {
		suffix := next + attempt
		if suffix > 9999 {
			return "", fmt.Errorf("backup: suffix range exhausted for %q (max .9999 in use)", base)
		}
		backupName := fmt.Sprintf("%s.%04d", base, suffix)
		backupPath := filepath.Join(dir, backupName)
		err := fsys.WriteFileExclMode(backupPath, data, info.Mode().Perm())
		if err == nil {
			return backupPath, nil
		}
		if errors.Is(err, fs.ErrExist) || os.IsExist(err) {
			continue
		}
		return "", fmt.Errorf("backup: failed to write %s: %w", backupPath, err)
	}
	return "", fmt.Errorf("backup: exhausted %d attempts to find unused suffix for %q", maxAttempts, base)
}

// nextBackupSuffix returns the next 4-digit suffix to use for base.
func nextBackupSuffix(base string, entries []os.DirEntry) (int, error) {
	re := regexp.MustCompile(`^` + regexp.QuoteMeta(base) + `\.(\d{4})$`)
	maxN := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := re.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		if n > maxN {
			maxN = n
		}
	}
	next := maxN + 1
	if next > 9999 {
		return 0, fmt.Errorf("backup: suffix range exhausted for %q (max .9999 in use)", base)
	}
	return next, nil
}
