// ClawEh
// License: MIT

// Package admin owns the operator credentials file (<CLAW_HOME>/credentials.json)
// and the `claw admin` command that writes it. The gateway reads the file to
// authenticate WebUI and API logins; nothing in the WebUI can create or change
// the account.
package admin

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters for new hashes: 64 MiB, 3 passes, 1 lane, 16-byte salt,
// 32-byte key. Verification reads the parameters from the hash string, so
// changing these only affects hashes written afterwards.
const (
	argonMemory  uint32 = 64 * 1024
	argonTime    uint32 = 3
	argonThreads uint8  = 1
	saltLen             = 16
	keyLen       uint32 = 32
)

// ErrInvalidHash reports a password hash that is not a well-formed argon2id
// PHC string.
var ErrInvalidHash = errors.New("invalid argon2id hash")

// b64 is the PHC string encoding: standard alphabet, no padding.
var b64 = base64.RawStdEncoding

// HashPassword derives an argon2id hash of password with a fresh random salt
// and returns it in PHC string format:
// $argon2id$v=19$m=65536,t=3,p=1$<salt>$<hash>.
func HashPassword(password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, keyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// phcHash is a parsed argon2id PHC string.
type phcHash struct {
	memory  uint32
	time    uint32
	threads uint8
	salt    []byte
	key     []byte
}

// ParseHash validates an argon2id PHC string. It is what Load uses to reject a
// corrupt credentials file up front rather than at the first login.
func ParseHash(s string) error {
	_, err := parsePHC(s)
	return err
}

func parsePHC(s string) (*phcHash, error) {
	parts := strings.Split(s, "$")
	// "" argon2id v=19 m=..,t=..,p=.. salt hash
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return nil, ErrInvalidHash
	}
	version, ok := strings.CutPrefix(parts[2], "v=")
	if !ok {
		return nil, ErrInvalidHash
	}
	if v, err := strconv.Atoi(version); err != nil || v != argon2.Version {
		return nil, fmt.Errorf("%w: unsupported version %q", ErrInvalidHash, version)
	}

	h := &phcHash{}
	for kv := range strings.SplitSeq(parts[3], ",") {
		k, v, found := strings.Cut(kv, "=")
		if !found {
			return nil, ErrInvalidHash
		}
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("%w: parameter %s", ErrInvalidHash, k)
		}
		switch k {
		case "m":
			h.memory = uint32(n)
		case "t":
			h.time = uint32(n)
		case "p":
			if n == 0 || n > 255 {
				return nil, fmt.Errorf("%w: parameter p", ErrInvalidHash)
			}
			h.threads = uint8(n)
		default:
			return nil, fmt.Errorf("%w: unknown parameter %s", ErrInvalidHash, k)
		}
	}
	if h.memory == 0 || h.time == 0 || h.threads == 0 {
		return nil, fmt.Errorf("%w: missing parameter", ErrInvalidHash)
	}

	var err error
	if h.salt, err = b64.DecodeString(parts[4]); err != nil || len(h.salt) == 0 {
		return nil, fmt.Errorf("%w: salt", ErrInvalidHash)
	}
	if h.key, err = b64.DecodeString(parts[5]); err != nil || len(h.key) == 0 {
		return nil, fmt.Errorf("%w: hash", ErrInvalidHash)
	}
	return h, nil
}

// VerifyPassword reports whether password matches the argon2id PHC string
// hash. A malformed hash returns ErrInvalidHash; a wrong password returns
// (false, nil). The comparison is constant time in the derived key.
func VerifyPassword(hash, password string) (bool, error) {
	h, err := parsePHC(hash)
	if err != nil {
		return false, err
	}
	key := argon2.IDKey([]byte(password), h.salt, h.time, h.memory, h.threads, uint32(len(h.key))) //nolint:gosec // G115: the key was base64-decoded from one PHC field, far below MaxUint32
	return subtle.ConstantTimeCompare(key, h.key) == 1, nil
}
