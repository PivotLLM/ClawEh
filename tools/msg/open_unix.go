//go:build unix

package msg

import (
	"os"
	"syscall"
)

// openNoFollow opens path for reading without following a symbolic link in
// its last component and without blocking (a FIFO or device swapped in is
// opened, then refused by the caller's checks, never waited on).
func openNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0) //nolint:gosec // the caller checked the agent may read path
}
