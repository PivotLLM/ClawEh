//go:build !unix

package files

import "os"

// readOpenFlags open a file for reading. Platforms without O_NONBLOCK open
// as usual.
const readOpenFlags = os.O_RDONLY
