//go:build unix

package forum

import (
	"errors"
	"os"
	"syscall"
)

// tryLockFile takes an exclusive lock on f without waiting. It returns
// errWouldBlock when another holder has it.
func tryLockFile(f *os.File) error {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return errWouldBlock
	}
	return err
}

// unlockFile drops the lock tryLockFile took on f.
func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
