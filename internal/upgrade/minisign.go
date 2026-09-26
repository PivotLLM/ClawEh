package upgrade

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/blake2b"
)

// Minisign (https://jedisct1.github.io/minisign/) verification, implemented
// here because the format is small and the alternative is a dependency for
// one function.
//
// Public key line:   base64("Ed" || keyID[8] || ed25519 public key[32])
// Signature file:
//
//	untrusted comment: <free text, not signed>
//	base64(alg[2] || keyID[8] || signature[64])
//	trusted comment: <free text, signed>
//	base64(global signature[64])
//
// alg "ED" (the only mode current minisign writes) signs BLAKE2b-512 of the
// file; the legacy "Ed" mode signs the raw file and is rejected. The global
// signature is Ed25519 over signature || trusted comment, which is what makes
// the trusted comment trusted.

const (
	minisignKeyAlg       = "Ed"
	minisignPrehashedAlg = "ED"
	minisignKeyIDLen     = 8
	minisignPubKeyLen    = 2 + minisignKeyIDLen + ed25519.PublicKeySize
	minisignSigLen       = 2 + minisignKeyIDLen + ed25519.SignatureSize
	untrustedPrefix      = "untrusted comment: "
	trustedPrefix        = "trusted comment: "
)

// errNoReleaseKey is returned when the binary was built without a release
// signing key. Nothing downloaded can be trusted then, so upgrading refuses.
var errNoReleaseKey = errors.New("no release signing key is embedded in this build; refusing to upgrade")

// minisignPublicKey is a parsed minisign public key.
type minisignPublicKey struct {
	keyID [minisignKeyIDLen]byte
	key   ed25519.PublicKey
}

// parseMinisignPublicKey accepts either the bare base64 key line or the full
// contents of a minisign .pub file (untrusted comment line followed by the key).
func parseMinisignPublicKey(s string) (*minisignPublicKey, error) {
	if strings.TrimSpace(s) == "" {
		return nil, errNoReleaseKey
	}
	keyLine := ""
	for line := range strings.SplitSeq(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, untrustedPrefix) {
			continue
		}
		keyLine = line
		break
	}
	raw, err := base64.StdEncoding.DecodeString(keyLine)
	if err != nil {
		return nil, fmt.Errorf("minisign public key is not valid base64: %w", err)
	}
	if len(raw) != minisignPubKeyLen {
		return nil, fmt.Errorf("minisign public key has %d bytes, want %d", len(raw), minisignPubKeyLen)
	}
	if string(raw[:2]) != minisignKeyAlg {
		return nil, fmt.Errorf("minisign public key algorithm %q is not %q", raw[:2], minisignKeyAlg)
	}
	pk := &minisignPublicKey{key: ed25519.PublicKey(raw[2+minisignKeyIDLen:])}
	copy(pk.keyID[:], raw[2:2+minisignKeyIDLen])
	return pk, nil
}

// verifyMinisign checks that sigFile (the contents of a .minisig file) is a
// valid signature over message by pk. Any deviation from the format is an
// error; there is no partial success.
func verifyMinisign(pk *minisignPublicKey, message, sigFile []byte) error {
	lines := strings.Split(strings.TrimRight(string(sigFile), "\r\n"), "\n")
	if len(lines) != 4 {
		return fmt.Errorf("signature file has %d lines, want 4", len(lines))
	}
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], "\r")
	}
	if !strings.HasPrefix(lines[0], untrustedPrefix) {
		return fmt.Errorf("signature file line 1 does not start with %q", untrustedPrefix)
	}
	if !strings.HasPrefix(lines[2], trustedPrefix) {
		return fmt.Errorf("signature file line 3 does not start with %q", trustedPrefix)
	}
	trustedComment := strings.TrimPrefix(lines[2], trustedPrefix)

	sigBlob, err := base64.StdEncoding.DecodeString(lines[1])
	if err != nil {
		return fmt.Errorf("signature is not valid base64: %w", err)
	}
	if len(sigBlob) != minisignSigLen {
		return fmt.Errorf("signature has %d bytes, want %d", len(sigBlob), minisignSigLen)
	}
	globalSig, err := base64.StdEncoding.DecodeString(lines[3])
	if err != nil {
		return fmt.Errorf("global signature is not valid base64: %w", err)
	}
	if len(globalSig) != ed25519.SignatureSize {
		return fmt.Errorf("global signature has %d bytes, want %d", len(globalSig), ed25519.SignatureSize)
	}

	alg := string(sigBlob[:2])
	if alg != minisignPrehashedAlg {
		return fmt.Errorf("signature algorithm %q is not supported (want %q)", alg, minisignPrehashedAlg)
	}
	if !bytes.Equal(sigBlob[2:2+minisignKeyIDLen], pk.keyID[:]) {
		return errors.New("signature was made with a different key than the embedded release key")
	}
	sig := sigBlob[2+minisignKeyIDLen:]

	digest := blake2b.Sum512(message)
	if !ed25519.Verify(pk.key, digest[:], sig) {
		return errors.New("signature does not match the file contents")
	}
	if !ed25519.Verify(pk.key, append(append([]byte{}, sig...), trustedComment...), globalSig) {
		return errors.New("global signature does not match (trusted comment was altered)")
	}
	return nil
}

// lookupChecksum returns the hex SHA-256 recorded for name in a sha256sum-style
// checksum list ("<hex>  <name>" per line; a leading "*" on the name, which
// sha256sum uses for binary mode, is ignored).
func lookupChecksum(checksums []byte, name string) (string, error) {
	for line := range strings.SplitSeq(string(checksums), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if strings.TrimPrefix(fields[1], "*") == name {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("%s is not listed in the signed checksums", name)
}

// verifySignedArchive verifies that archivePath's SHA-256 matches the entry
// for archiveName in checksums, after verifying that checksums itself carries a
// valid minisign signature (sigFile) from the embedded release key.
func verifySignedArchive(releaseKey string, checksums, sigFile []byte, archiveName, archivePath string) error {
	pk, err := parseMinisignPublicKey(releaseKey)
	if err != nil {
		return err
	}
	if err = verifyMinisign(pk, checksums, sigFile); err != nil {
		return fmt.Errorf("checksums.txt signature: %w", err)
	}
	expected, err := lookupChecksum(checksums, archiveName)
	if err != nil {
		return err
	}
	actual, err := computeSHA256(archivePath)
	if err != nil {
		return fmt.Errorf("calculating archive checksum: %w", err)
	}
	if !strings.EqualFold(expected, actual) {
		return fmt.Errorf("SHA256 checksum mismatch for %s!\n  Signed:   %s\n  Actual:   %s\nThe download is corrupted or has been tampered with", archiveName, expected, actual)
	}
	return nil
}
