package gatewayproto

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"testing"
)

func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// signAs mimics a device client: build the v3 canonical string and sign it.
func signAs(t *testing.T, priv ed25519.PrivateKey, in DeviceAuthInput) string {
	t.Helper()
	return b64url(ed25519.Sign(priv, []byte(BuildDeviceAuthPayloadV3(in))))
}

func TestBuildDeviceAuthPayloadV3_Exact(t *testing.T) {
	in := DeviceAuthInput{
		DeviceID: "dev1", ClientID: "rabbit-r1", ClientMode: "node", Role: "node",
		Scopes: []string{"a", "b"}, SignedAtMs: 1782518400000, Token: "tok",
		Nonce: "nonce1", Platform: "iOS", DeviceFamily: "R1",
	}
	got := BuildDeviceAuthPayloadV3(in)
	// platform/deviceFamily lowercased; scopes comma-joined; pipe-delimited; fixed order.
	want := "v3|dev1|rabbit-r1|node|node|a,b|1782518400000|tok|nonce1|ios|r1"
	if got != want {
		t.Fatalf("v3 payload mismatch:\n got=%q\nwant=%q", got, want)
	}
}

func TestVerifyConnectSignature_V3RoundTrip(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	in := DeviceAuthInput{
		DeviceID: DeviceIDFromPublicKey(pub), ClientID: "rabbit-r1", ClientMode: "node",
		Role: "node", Scopes: nil, SignedAtMs: 1782518400000, Token: "shared",
		Nonce: "server-nonce", Platform: "rabbit", DeviceFamily: "r1",
	}
	sig := signAs(t, priv, in)
	if v := VerifyConnectSignature(in, b64url(pub), sig); v != "v3" {
		t.Fatalf("expected v3 match, got %q", v)
	}
	// Tampering with any signed field must invalidate.
	bad := in
	bad.Nonce = "different-nonce"
	if v := VerifyConnectSignature(bad, b64url(pub), sig); v != "" {
		t.Fatalf("expected no match after nonce tamper, got %q", v)
	}
}

func TestVerifyConnectSignature_V2Fallback(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	in := DeviceAuthInput{
		DeviceID: "d", ClientID: "c", ClientMode: "node", Role: "node",
		Scopes: []string{"x"}, SignedAtMs: 1, Token: "", Nonce: "n",
		Platform: "p", DeviceFamily: "f",
	}
	// Sign the v2 payload (legacy client).
	sig := b64url(ed25519.Sign(priv, []byte(BuildDeviceAuthPayloadV2(in))))
	if v := VerifyConnectSignature(in, b64url(pub), sig); v != "v2" {
		t.Fatalf("expected v2 fallback match, got %q", v)
	}
}

func TestDeviceIDFromPublicKey(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(pub)
	if got, want := DeviceIDFromPublicKey(pub), hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("device id mismatch: got %q want %q", got, want)
	}
}

func TestVerifyDeviceSignature_BadInputs(t *testing.T) {
	if VerifyDeviceSignature("not-base64-!!", "p", "sig") {
		t.Fatal("expected false for undecodable key")
	}
	if VerifyDeviceSignature(b64url([]byte("too-short")), "p", b64url([]byte("x"))) {
		t.Fatal("expected false for wrong key length")
	}
}
