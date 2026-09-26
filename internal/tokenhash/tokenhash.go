// ClawEh
// License: MIT

// Package tokenhash is the one way a bearer token is stored at rest. A token a
// client presents (an MCP service token, a device-gateway token) is never
// needed in plaintext again once it has been handed out, so the stores keep
// SHA-256(token) and hash the presented value to look it up. The stored form
// carries a fixed prefix so a store can tell a hashed value from a plaintext
// one written by an earlier release and migrate it in place.
package tokenhash

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Prefix marks a stored value as a hash. No plaintext token starts with it:
// service tokens start with "SST" and device tokens are bare hex.
const Prefix = "sha256:"

// Hash returns the stored form of a plaintext token.
func Hash(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return Prefix + hex.EncodeToString(sum[:])
}

// IsHashed reports whether a stored value is already in Hash's form.
func IsHashed(stored string) bool {
	return strings.HasPrefix(stored, Prefix)
}
