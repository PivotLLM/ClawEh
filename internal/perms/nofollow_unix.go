//go:build unix

package perms

import "syscall"

// oNoFollow makes an open fail on a symbolic link.
const oNoFollow = syscall.O_NOFOLLOW
