package gateway

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/utils"
)

const lockFileName = "claw.lock"

// acquireLock creates and exclusively locks a PID file in the given base directory.
// It returns the open file handle so the caller can defer releaseLock.
// If another instance already holds the lock, it returns a descriptive error and the
// caller should exit immediately — no retries, no fallback.
func acquireLock(baseDir string) (*os.File, error) {
	lockPath := filepath.Join(baseDir, lockFileName)

	// Ensure the base directory exists before attempting to create the lock file.
	if err := os.MkdirAll(baseDir, 0o700); err != nil {
		return nil, fmt.Errorf("cannot create base directory %q: %w", baseDir, err)
	}

	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // lock file under the configured data directory
	if err != nil {
		return nil, fmt.Errorf("cannot open lock file %q: %w", lockPath, err)
	}

	// Non-blocking exclusive advisory lock. If a second instance is running it will
	// already hold this lock and Flock returns EWOULDBLOCK immediately.
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		utils.CloseQuietly(f)
		return nil, fmt.Errorf("another instance is already running (lock held on %q): %w", lockPath, err)
	}

	// Write current PID so external tooling can inspect it.
	if err := f.Truncate(0); err == nil {
		if _, writeErr := fmt.Fprintf(f, "%d\n", os.Getpid()); writeErr != nil {
			logger.WarnCF("gateway", "failed to write PID to lock file", map[string]any{"path": lockPath, "error": writeErr.Error()})
		}
	}

	return f, nil
}

// releaseLock releases the advisory lock and removes the lock file.
// Intended to be called via defer immediately after a successful acquireLock.
func releaseLock(f *os.File) {
	path := f.Name()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
		logger.WarnCF("gateway", "failed to release lock file", map[string]any{"path": path, "error": err.Error()})
	}
	if err := f.Close(); err != nil {
		logger.WarnCF("gateway", "failed to close lock file", map[string]any{"path": path, "error": err.Error()})
	}
	if err := os.Remove(path); err != nil {
		logger.WarnCF("gateway", "failed to remove lock file", map[string]any{"path": path, "error": err.Error()})
	}
}
