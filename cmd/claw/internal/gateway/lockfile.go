package gateway

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

const lockFileName = "claw.lock"

// acquireLock creates and exclusively locks a PID file in the given base directory.
// It returns the open file handle so the caller can defer releaseLock.
// If another instance already holds the lock, it returns a descriptive error and the
// caller should exit immediately — no retries, no fallback.
func acquireLock(baseDir string) (*os.File, error) {
	lockPath := filepath.Join(baseDir, lockFileName)

	// Ensure the base directory exists before attempting to create the lock file.
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return nil, fmt.Errorf("cannot create base directory %q: %w", baseDir, err)
	}

	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("cannot open lock file %q: %w", lockPath, err)
	}

	// Non-blocking exclusive advisory lock. If a second instance is running it will
	// already hold this lock and Flock returns EWOULDBLOCK immediately.
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("another instance is already running (lock held on %q): %w", lockPath, err)
	}

	// Write current PID so external tooling can inspect it.
	if err := f.Truncate(0); err == nil {
		_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
	}

	return f, nil
}

// releaseLock releases the advisory lock and removes the lock file.
// Intended to be called via defer immediately after a successful acquireLock.
func releaseLock(f *os.File) {
	path := f.Name()
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	_ = f.Close()
	_ = os.Remove(path)
}
