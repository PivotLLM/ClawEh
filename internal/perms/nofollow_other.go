//go:build !unix

package perms

// oNoFollow is 0 where the platform has no O_NOFOLLOW; EnsurePrivateFile
// does nothing there.
const oNoFollow = 0
