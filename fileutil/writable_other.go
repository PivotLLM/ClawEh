// ClawEh
// License: MIT

//go:build !unix

package fileutil

// Writable reports true: without access(2) the write itself decides.
func Writable(string) bool { return true }
