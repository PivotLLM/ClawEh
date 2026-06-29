package gatewayproto

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strconv"
	"strings"
)

// DeviceAuthInput is the structured input the gateway reconstructs from connect
// params to verify a device signature. The server NEVER trusts a client-serialized
// payload — it rebuilds the canonical string from these fields and verifies.
type DeviceAuthInput struct {
	DeviceID     string
	ClientID     string
	ClientMode   string
	Role         string
	Scopes       []string
	SignedAtMs   int64
	Token        string // resolved signature token: auth.token ?? deviceToken ?? bootstrapToken ?? ""
	Nonce        string
	Platform     string
	DeviceFamily string
}

// BuildDeviceAuthPayloadV3 builds the canonical v3 signing string (device-auth.ts
// buildDeviceAuthPayloadV3): pipe-joined, fixed field order, scopes comma-joined in
// caller order, platform/deviceFamily normalized. This exact byte string is signed
// by the client and re-derived + byte-compared by the server.
//
//	v3 | deviceId | clientId | clientMode | role | scopesCsv | signedAtMs | token | nonce | platform | deviceFamily
func BuildDeviceAuthPayloadV3(in DeviceAuthInput) string {
	return strings.Join([]string{
		"v3",
		in.DeviceID,
		in.ClientID,
		in.ClientMode,
		in.Role,
		strings.Join(in.Scopes, ","),
		strconv.FormatInt(in.SignedAtMs, 10),
		in.Token,
		in.Nonce,
		normalizeDeviceMetadataForAuth(in.Platform),
		normalizeDeviceMetadataForAuth(in.DeviceFamily),
	}, "|")
}

// BuildDeviceAuthPayloadV2 builds the legacy v2 signing string (no platform/deviceFamily).
// The server tries v3 first then falls back to v2.
func BuildDeviceAuthPayloadV2(in DeviceAuthInput) string {
	return strings.Join([]string{
		"v2",
		in.DeviceID,
		in.ClientID,
		in.ClientMode,
		in.Role,
		strings.Join(in.Scopes, ","),
		strconv.FormatInt(in.SignedAtMs, 10),
		in.Token,
		in.Nonce,
	}, "|")
}

// normalizeDeviceMetadataForAuth trims and lowercases ASCII A-Z only (device-auth.ts
// normalizeDeviceMetadataForAuth). Signatures are byte-compared, so the input is
// normalized identically on both sides to tolerate case differences.
func normalizeDeviceMetadataForAuth(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

// DeviceIDFromPublicKey derives the device id: lowercase hex SHA-256 of the raw
// 32-byte Ed25519 public key (device-identity.ts fingerprintPublicKey).
func DeviceIDFromPublicKey(rawPub []byte) string {
	sum := sha256.Sum256(rawPub)
	return hex.EncodeToString(sum[:])
}

// DecodeKeyOrSig decodes a base64url (unpadded) value, falling back to standard
// base64. Device public keys and signatures are emitted as raw base64url, no padding.
func DecodeKeyOrSig(s string) ([]byte, bool) {
	for _, enc := range []*base64.Encoding{
		base64.RawURLEncoding, base64.URLEncoding,
		base64.RawStdEncoding, base64.StdEncoding,
	} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, true
		}
	}
	return nil, false
}

// VerifyDeviceSignature verifies sig over payload using the raw base64url Ed25519
// public key. Returns false on any decode/length/verify failure.
func VerifyDeviceSignature(publicKeyB64, payload, signatureB64 string) bool {
	pub, ok := DecodeKeyOrSig(publicKeyB64)
	if !ok || len(pub) != ed25519.PublicKeySize {
		return false
	}
	sig, ok := DecodeKeyOrSig(signatureB64)
	if !ok {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(pub), []byte(payload), sig)
}

// VerifyConnectSignature verifies a connect device signature against both the v3
// and legacy v2 canonical payloads, returning the matched version ("v3"/"v2") or "".
func VerifyConnectSignature(in DeviceAuthInput, publicKeyB64, signatureB64 string) string {
	if VerifyDeviceSignature(publicKeyB64, BuildDeviceAuthPayloadV3(in), signatureB64) {
		return "v3"
	}
	if VerifyDeviceSignature(publicKeyB64, BuildDeviceAuthPayloadV2(in), signatureB64) {
		return "v2"
	}
	return ""
}
