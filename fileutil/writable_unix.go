// ClawEh
// License: MIT

//go:build unix

package fileutil

import "golang.org/x/sys/unix"

// Writable reports whether the process's account may write path, as the
// kernel decides it now (access(2) with W_OK).
func Writable(path string) bool {
	return unix.Access(path, unix.W_OK) == nil
}
