//go:build unix

package files

import (
	"os"
	"syscall"
)

// readOpenFlags open a file for reading without waiting (a FIFO or device
// is opened at once, then refused as not a regular file, instead of
// blocking until a writer appears) and without making a terminal the
// process's controlling terminal.
const readOpenFlags = os.O_RDONLY | syscall.O_NONBLOCK | syscall.O_NOCTTY

// hostOpenFlags are readOpenFlags for a host path whose links were resolved
// before the open: a link swapped in at the last moment is refused.
const hostOpenFlags = readOpenFlags | syscall.O_NOFOLLOW
