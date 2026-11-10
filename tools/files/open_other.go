//go:build !unix

package files

import "os"

// readOpenFlags open a file for reading. Platforms without O_NONBLOCK open
// as usual; the regular-file check after the open still applies.
const readOpenFlags = os.O_RDONLY

// hostOpenFlags are readOpenFlags; such platforms rely on the same-file
// check after the open.
const hostOpenFlags = readOpenFlags
