// Package fdcheck lets channel tests check that a file a send opened was
// closed again.
package fdcheck

import (
	"os"
	"path/filepath"
	"runtime"
)

// OpenFor reports whether this process holds a descriptor open on path. ok is
// false where the platform does not list its descriptors (anything but Linux).
func OpenFor(path string) (open, ok bool) {
	if runtime.GOOS != "linux" {
		return false, false
	}
	want, err := filepath.EvalSymlinks(path)
	if err != nil {
		want = path
	}
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return false, false
	}
	for _, e := range entries {
		target, err := os.Readlink(filepath.Join("/proc/self/fd", e.Name()))
		if err == nil && (target == want || target == path) {
			return true, true
		}
	}
	return false, true
}
