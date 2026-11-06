//go:build !unix

package perms

import (
	"errors"
	"io/fs"
)

// oNoFollow is 0 where the platform has no O_NOFOLLOW (outside the unix
// build tag). It only lets the package compile there: EnsureFileMode and
// Enforce return before touching anything on Windows, and the release
// targets are all unix.
const oNoFollow = 0

// chmodSafe is never reached outside unix (see oNoFollow).
func chmodSafe(string, fs.FileMode, fs.FileInfo) error {
	return errors.New("perms: not supported on this platform")
}

// linkCount is 1 outside unix.
func linkCount(fs.FileInfo) uint64 { return 1 }
