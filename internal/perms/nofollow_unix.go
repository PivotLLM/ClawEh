//go:build unix

package perms

import (
	"errors"
	"io/fs"
	"os"
	"syscall"
)

// oNoFollow makes an open fail on a symbolic link.
const oNoFollow = syscall.O_NOFOLLOW

// errReplaced is returned by chmodSafe when the path no longer names the
// entry that was examined (it was swapped for another file or a link).
var errReplaced = errors.New("replaced since it was examined")

// chmodSafe sets the entry at path to exactly want, without following a
// symbolic link and only if it is still the entry walked describes: the last
// component is opened with O_NOFOLLOW (and O_DIRECTORY for a directory,
// O_NONBLOCK so a swapped-in FIFO cannot block), the open descriptor is
// compared with walked (same file, same type), and the mode is set on the
// descriptor. A path swapped for a link or another file between the walk and
// the change is refused, so nothing outside the data directory is changed. A
// file the owner cannot read is opened for writing instead (nothing is
// written).
func chmodSafe(path string, want fs.FileMode, walked fs.FileInfo) error {
	flags := syscall.O_NOFOLLOW | syscall.O_NONBLOCK | syscall.O_CLOEXEC
	if walked.IsDir() {
		flags |= syscall.O_DIRECTORY
	}
	f, err := os.OpenFile(path, os.O_RDONLY|flags, 0) //nolint:gosec // a path under the data directory, opened without following links
	if err != nil && errors.Is(err, fs.ErrPermission) && !walked.IsDir() {
		f, err = os.OpenFile(path, os.O_WRONLY|flags, 0) //nolint:gosec // as above; nothing is written
	}
	if err != nil {
		return err
	}
	defer f.Close() //nolint:errcheck // read-only use of the descriptor
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(fi, walked) || fi.Mode().Type() != walked.Mode().Type() {
		return errReplaced
	}
	return f.Chmod(want.Perm())
}

// linkCount is the number of hard links to the file fi describes, or 1 when
// the platform does not say.
func linkCount(fi fs.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Nlink) //nolint:unconvert // Nlink is uint16 on darwin, uint64 on linux
	}
	return 1
}
