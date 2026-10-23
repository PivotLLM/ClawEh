//go:build !unix

package perms

// oNoFollow is 0 where the platform has no O_NOFOLLOW (outside the unix
// build tag). It only lets the package compile there: EnsurePrivateFile
// returns before opening anything on Windows, and the release targets are
// all unix.
const oNoFollow = 0
