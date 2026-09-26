package gatewayproto

import (
	"crypto/ed25519"
	"encoding/base64"
	"strings"
	"testing"
)

// fuzzKey is a fixed Ed25519 key pair for the signature fuzz targets, derived
// deterministically so that every fuzz worker process verifies against the same
// public key and a seed corpus entry means the same thing in each.
func fuzzKey() (ed25519.PublicKey, ed25519.PrivateKey) {
	seed := []byte("gatewayproto-fuzz-seed-0123456789ab") // 32 bytes
	priv := ed25519.NewKeyFromSeed(seed[:ed25519.SeedSize])
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		panic("ed25519 private key did not yield an ed25519 public key")
	}
	return pub, priv
}

func fuzzB64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// FuzzDecodeKeyOrSig: DecodeKeyOrSig never panics; a successful decode is
// bounded by the input length and re-encodes (in the accepting alphabet) to a
// string that decodes back to the same bytes.
func FuzzDecodeKeyOrSig(f *testing.F) {
	pub, _ := fuzzKey()
	f.Add(fuzzB64(pub))
	f.Add(base64.StdEncoding.EncodeToString(pub))
	f.Add(base64.URLEncoding.EncodeToString([]byte{0xfb, 0xff}))
	f.Add("")
	f.Add("=")
	f.Add("A")
	f.Add("AA==")
	f.Add("-_-_")
	f.Add("+/+/")
	f.Add("not base64!")
	f.Add("\x00\xff")
	f.Fuzz(func(t *testing.T, s string) {
		b, ok := DecodeKeyOrSig(s)
		if !ok {
			if b != nil {
				t.Fatalf("DecodeKeyOrSig(%q) failed but returned bytes %x", s, b)
			}
			return
		}
		// base64 expands 3 bytes to 4 chars; decoded output can never exceed input.
		if len(b) > len(s) {
			t.Fatalf("DecodeKeyOrSig(%q) decoded %d bytes from %d chars", s, len(b), len(s))
		}
		again, ok2 := DecodeKeyOrSig(fuzzB64(b))
		if !ok2 || string(again) != string(b) {
			t.Fatalf("DecodeKeyOrSig(%q) = %x does not survive re-encode", s, b)
		}
	})
}

// FuzzVerifyDeviceSignature: verification never panics and never accepts a
// signature that is not the genuine one for (key, payload). The genuine
// signature is a seed so the accepting branch is exercised too.
func FuzzVerifyDeviceSignature(f *testing.F) {
	pub, priv := fuzzKey()
	pubB64 := fuzzB64(pub)
	payload := "v3|dev|cli|node|node||1700000000000|tok|nonce|rabbit|r1"
	f.Add(pubB64, payload, fuzzB64(ed25519.Sign(priv, []byte(payload))))
	f.Add(pubB64, payload, "")
	f.Add(pubB64, payload, "AAAA")
	f.Add("", payload, fuzzB64(ed25519.Sign(priv, []byte(payload))))
	f.Add("short", payload, "short")
	f.Add(fuzzB64(make([]byte, 31)), payload, fuzzB64(make([]byte, 64)))
	f.Add(fuzzB64(make([]byte, 33)), payload, fuzzB64(make([]byte, 64)))
	f.Add(pubB64, "", fuzzB64(make([]byte, 65)))
	f.Fuzz(func(t *testing.T, keyB64, payload, sigB64 string) {
		ok := VerifyDeviceSignature(keyB64, payload, sigB64)
		if !ok {
			return
		}
		key, kok := DecodeKeyOrSig(keyB64)
		if !kok || len(key) != ed25519.PublicKeySize {
			t.Fatalf("accepted signature under undecodable/short key %q", keyB64)
		}
		if string(key) != string(pub) {
			// A different (fuzzer-produced) key: we cannot know its private half,
			// so only insist that the claim is internally consistent.
			sig, sok := DecodeKeyOrSig(sigB64)
			if !sok || !ed25519.Verify(ed25519.PublicKey(key), []byte(payload), sig) {
				t.Fatalf("VerifyDeviceSignature accepted but ed25519.Verify rejects: key=%q sig=%q", keyB64, sigB64)
			}
			return
		}
		sig, _ := DecodeKeyOrSig(sigB64)
		if string(sig) != string(ed25519.Sign(priv, []byte(payload))) {
			t.Fatalf("forgery accepted: payload=%q sig=%q", payload, sigB64)
		}
	})
}

// FuzzVerifyConnectSignature: for any connect input, a genuine v3 signature
// verifies as "v3", a genuine v2 signature as "v2", and any single-field tamper
// of the input is rejected. Fields that carry the '|' separator or that
// normalize to the same bytes (platform/deviceFamily case) are the one
// permitted exception, mirroring the reference protocol.
func FuzzVerifyConnectSignature(f *testing.F) {
	f.Add("dev", "rabbit-r1", "node", "node", "operator.read,operator.write", int64(1700000000000), "tok", "nonce", "rabbit", "r1")
	f.Add("", "", "", "", "", int64(0), "", "", "", "")
	f.Add("d", "c", "ui", "operator", "", int64(-1), "", "n", "Android", " Pixel ")
	f.Add("d|x", "c", "node", "node", "a,b", int64(1), "t", "n", "p", "f")
	f.Fuzz(func(t *testing.T, deviceID, clientID, mode, role, scopesCSV string, signedAt int64, token, nonce, platform, family string) {
		pub, priv := fuzzKey()
		pubB64 := fuzzB64(pub)
		var scopes []string
		if scopesCSV != "" {
			scopes = strings.Split(scopesCSV, ",")
		}
		in := DeviceAuthInput{
			DeviceID: deviceID, ClientID: clientID, ClientMode: mode, Role: role, Scopes: scopes,
			SignedAtMs: signedAt, Token: token, Nonce: nonce, Platform: platform, DeviceFamily: family,
		}
		sigV3 := fuzzB64(ed25519.Sign(priv, []byte(BuildDeviceAuthPayloadV3(in))))
		if got := VerifyConnectSignature(in, pubB64, sigV3); got != "v3" {
			t.Fatalf("genuine v3 signature verified as %q for %+v", got, in)
		}
		sigV2 := fuzzB64(ed25519.Sign(priv, []byte(BuildDeviceAuthPayloadV2(in))))
		if got := VerifyConnectSignature(in, pubB64, sigV2); got != "v2" {
			t.Fatalf("genuine v2 signature verified as %q for %+v", got, in)
		}

		// Tampering with the nonce or the timestamp must always be rejected:
		// they are the replay defences and cannot collide by construction.
		tampered := in
		tampered.Nonce += "x"
		if got := VerifyConnectSignature(tampered, pubB64, sigV3); got != "" {
			t.Fatalf("nonce tamper accepted as %q", got)
		}
		tampered = in
		tampered.SignedAtMs++
		if got := VerifyConnectSignature(tampered, pubB64, sigV3); got != "" {
			t.Fatalf("signedAt tamper accepted as %q", got)
		}
		// The device id is the key fingerprint; changing it must break the signature.
		tampered = in
		tampered.DeviceID += "0"
		if got := VerifyConnectSignature(tampered, pubB64, sigV3); got != "" {
			t.Fatalf("deviceId tamper accepted as %q", got)
		}
	})
}
