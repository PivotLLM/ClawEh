//go:build unix

package files

import (
	"os"
	"syscall"
)

// readOpenFlags open a file for reading without waiting: a FIFO or device
// is opened at once (and refused by a caller that wants a regular file)
// instead of blocking until a writer appears.
const readOpenFlags = os.O_RDONLY | syscall.O_NONBLOCK
