//go:build !unix

package msg

import "os"

// openNoFollow opens path for reading. Platforms without O_NOFOLLOW rely on
// the caller's same-file check alone.
func openNoFollow(path string) (*os.File, error) {
	return os.Open(path) //nolint:gosec // the caller checked the agent may read path
}
