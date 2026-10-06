//go:build windows

package forum

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// errSymlink refuses a path whose last element is a symbolic link.
var errSymlink = errors.New("is a symbolic link")

// tryLockFile takes an exclusive lock on f without waiting. It returns
// errWouldBlock when another holder has it.
func tryLockFile(f *os.File) error {
	var ol windows.Overlapped
	err := windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &ol)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return errWouldBlock
	}
	return err
}

// unlockFile drops the lock tryLockFile took on f.
func unlockFile(f *os.File) error {
	var ol windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &ol)
}

// openNoFollow opens path read-only, failing when its last element is a
// symbolic link. Windows has no O_NOFOLLOW: path is checked with Lstat,
// and the opened file must be the one checked, so a link swapped in
// between fails.
func openNoFollow(path string) (*os.File, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if before.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s: %w", path, errSymlink)
	}
	f, err := os.Open(path) //nolint:gosec // callers pass a path they have checked
	if err != nil {
		return nil, err
	}
	opened, err := f.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return nil, errors.Join(fmt.Errorf("%s: %w", path, errSymlink), err, f.Close())
	}
	return f, nil
}
