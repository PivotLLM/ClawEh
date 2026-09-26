package upgrade

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/blake2b"
)

// testSigner is an in-test minisign keypair: it writes the public key line and
// signature files in the exact format `minisign -G` / `minisign -S` produce.
type testSigner struct {
	keyID [8]byte
	pub   ed25519.PublicKey
	priv  ed25519.PrivateKey
}

func newTestSigner(t *testing.T) *testSigner {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s := &testSigner{pub: pub, priv: priv}
	if _, err := rand.Read(s.keyID[:]); err != nil {
		t.Fatal(err)
	}
	return s
}

// publicKeyLine is the base64 line of a minisign.pub file.
func (s *testSigner) publicKeyLine() string {
	raw := append(append([]byte("Ed"), s.keyID[:]...), s.pub...)
	return base64.StdEncoding.EncodeToString(raw)
}

// publicKeyFile is a whole minisign.pub file, comment line included.
func (s *testSigner) publicKeyFile() string {
	return "untrusted comment: minisign public key TEST\n" + s.publicKeyLine() + "\n"
}

// sign returns the contents of a .minisig file for message (prehashed mode).
func (s *testSigner) sign(message []byte, trustedComment string) []byte {
	return s.signWith(s.keyID, "ED", message, trustedComment)
}

func (s *testSigner) signWith(keyID [8]byte, alg string, message []byte, trustedComment string) []byte {
	var signed []byte
	if alg == "ED" {
		d := blake2b.Sum512(message)
		signed = d[:]
	} else {
		signed = message
	}
	sig := ed25519.Sign(s.priv, signed)
	sigBlob := append(append([]byte(alg), keyID[:]...), sig...)
	globalSig := ed25519.Sign(s.priv, append(append([]byte{}, sig...), trustedComment...))
	return []byte("untrusted comment: signature from minisign secret key\n" +
		base64.StdEncoding.EncodeToString(sigBlob) + "\n" +
		"trusted comment: " + trustedComment + "\n" +
		base64.StdEncoding.EncodeToString(globalSig) + "\n")
}

func TestParseMinisignPublicKey(t *testing.T) {
	s := newTestSigner(t)

	for _, input := range []string{s.publicKeyLine(), s.publicKeyFile(), "  " + s.publicKeyLine() + "\n"} {
		pk, err := parseMinisignPublicKey(input)
		if err != nil {
			t.Fatalf("parseMinisignPublicKey(%q) error = %v", input, err)
		}
		if pk.keyID != s.keyID || !pk.key.Equal(s.pub) {
			t.Errorf("parsed key does not match the generated one")
		}
	}

	bad := []struct {
		name, key string
	}{
		{"empty", ""},
		{"whitespace", " \n"},
		{"not base64", "not*base64"},
		{"wrong length", base64.StdEncoding.EncodeToString([]byte("Ed12345678short"))},
		{"wrong algorithm", base64.StdEncoding.EncodeToString(append(append([]byte("RS"), s.keyID[:]...), s.pub...))},
	}
	for _, tc := range bad {
		if _, err := parseMinisignPublicKey(tc.key); err == nil {
			t.Errorf("%s: expected error", tc.name)
		}
	}
	if _, err := parseMinisignPublicKey(""); err != errNoReleaseKey { //nolint:errorlint // sentinel returned directly
		t.Errorf("empty key error = %v, want errNoReleaseKey", err)
	}
}

func TestVerifyMinisign(t *testing.T) {
	s := newTestSigner(t)
	pk, err := parseMinisignPublicKey(s.publicKeyLine())
	if err != nil {
		t.Fatal(err)
	}
	msg := []byte("abc123  claw-linux-amd64.tar.gz\n")
	good := s.sign(msg, "timestamp:1700000000\tfile:checksums.txt\thashed")

	if err := verifyMinisign(pk, msg, good); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}

	other := newTestSigner(t)
	var otherID [8]byte
	copy(otherID[:], other.keyID[:])

	// Global signature made by another key but the same key ID: the body
	// verifies, the trusted comment does not.
	lines := strings.Split(strings.TrimRight(string(good), "\n"), "\n")
	swappedGlobal := []byte(strings.Join([]string{lines[0], lines[1], lines[2], base64.StdEncoding.EncodeToString(make([]byte, 64))}, "\n") + "\n")
	alteredComment := []byte(strings.Join([]string{lines[0], lines[1], "trusted comment: something else", lines[3]}, "\n") + "\n")

	cases := []struct {
		name string
		msg  []byte
		sig  []byte
	}{
		{"tampered message", []byte("deadbeef  claw-linux-amd64.tar.gz\n"), good},
		{"wrong key", msg, other.sign(msg, "x")},
		{"key id mismatch", msg, s.signWith(otherID, "ED", msg, "x")},
		{"legacy raw-message algorithm", msg, s.signWith(s.keyID, "Ed", msg, "x")},
		{"altered trusted comment", msg, alteredComment},
		{"bad global signature", msg, swappedGlobal},
		{"empty", msg, nil},
		{"three lines", msg, []byte(strings.Join(lines[:3], "\n"))},
		{"missing untrusted prefix", msg, []byte(strings.Join([]string{"comment", lines[1], lines[2], lines[3]}, "\n"))},
		{"missing trusted prefix", msg, []byte(strings.Join([]string{lines[0], lines[1], "comment", lines[3]}, "\n"))},
		{"garbage signature", msg, []byte(strings.Join([]string{lines[0], "!!!", lines[2], lines[3]}, "\n"))},
		{"short signature", msg, []byte(strings.Join([]string{lines[0], base64.StdEncoding.EncodeToString([]byte("ED")), lines[2], lines[3]}, "\n"))},
		{"garbage global", msg, []byte(strings.Join([]string{lines[0], lines[1], lines[2], "!!!"}, "\n"))},
	}
	for _, tc := range cases {
		if err := verifyMinisign(pk, tc.msg, tc.sig); err == nil {
			t.Errorf("%s: expected verification to fail", tc.name)
		}
	}
}

func TestLookupChecksum(t *testing.T) {
	list := []byte("aaaa  claw-linux-amd64.tar.gz\nbbbb *claw-darwin-arm64.tar.gz\n\ncccc\n")
	cases := []struct {
		name, want string
		wantErr    bool
	}{
		{"claw-linux-amd64.tar.gz", "aaaa", false},
		{"claw-darwin-arm64.tar.gz", "bbbb", false},
		{"claw-windows-amd64.tar.gz", "", true},
		{"cccc", "", true},
	}
	for _, tc := range cases {
		got, err := lookupChecksum(list, tc.name)
		if (err != nil) != tc.wantErr {
			t.Errorf("lookupChecksum(%q) error = %v, wantErr %v", tc.name, err, tc.wantErr)
		}
		if got != tc.want {
			t.Errorf("lookupChecksum(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestVerifySignedArchive(t *testing.T) {
	s := newTestSigner(t)
	dir := t.TempDir()
	archive := filepath.Join(dir, "claw-linux-amd64.tar.gz")
	content := []byte("pretend this is a tarball")
	if err := os.WriteFile(archive, content, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	checksums := []byte(hex.EncodeToString(sum[:]) + "  claw-linux-amd64.tar.gz\n" +
		strings.Repeat("0", 64) + "  claw-darwin-arm64.tar.gz\n")
	sig := s.sign(checksums, "ClawEh v0.6.0")

	if err := verifySignedArchive(s.publicKeyLine(), checksums, sig, "claw-linux-amd64.tar.gz", archive); err != nil {
		t.Fatalf("valid release rejected: %v", err)
	}

	// Attacker edits a checksum line after signing.
	tampered := []byte(strings.Replace(string(checksums), hex.EncodeToString(sum[:]), strings.Repeat("f", 64), 1))
	if err := verifySignedArchive(s.publicKeyLine(), tampered, sig, "claw-linux-amd64.tar.gz", archive); err == nil {
		t.Error("tampered checksums.txt accepted")
	}

	// Attacker re-signs the tampered list with their own key.
	if err := verifySignedArchive(s.publicKeyLine(), tampered, newTestSigner(t).sign(tampered, ""), "claw-linux-amd64.tar.gz", archive); err == nil {
		t.Error("checksums.txt signed by a foreign key accepted")
	}

	// Signed list is genuine but the archive was swapped.
	if err := os.WriteFile(archive, []byte("something else"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifySignedArchive(s.publicKeyLine(), checksums, sig, "claw-linux-amd64.tar.gz", archive); err == nil {
		t.Error("archive that does not match its signed checksum accepted")
	}

	// Archive not listed.
	if err := verifySignedArchive(s.publicKeyLine(), checksums, sig, "claw-netbsd-amd64.tar.gz", archive); err == nil {
		t.Error("unlisted archive accepted")
	}

	// Missing signature / no embedded key: both fatal.
	if err := verifySignedArchive(s.publicKeyLine(), checksums, nil, "claw-linux-amd64.tar.gz", archive); err == nil {
		t.Error("missing signature accepted")
	}
	if err := verifySignedArchive("", checksums, sig, "claw-linux-amd64.tar.gz", archive); err == nil {
		t.Error("verification without an embedded key succeeded")
	}
}

// TestReleasePublicKey pins the embedded key's state: either unset (upgrades
// fail closed) or a well-formed minisign public key.
func TestReleasePublicKey(t *testing.T) {
	pk, err := parseMinisignPublicKey(releasePublicKey)
	switch {
	case releasePublicKey == "":
		if err != errNoReleaseKey { //nolint:errorlint // sentinel returned directly
			t.Fatalf("unset key: error = %v, want errNoReleaseKey", err)
		}
	case err != nil:
		t.Fatalf("embedded release key does not parse: %v", err)
	case pk == nil:
		t.Fatal("embedded release key parsed to nil")
	}
}

func TestFindReleaseAssets(t *testing.T) {
	full := []GitHubAsset{
		{Name: "claw-linux-amd64.tar.gz"},
		{Name: "claw-linux-amd64.tar.gz.sha256"},
		{Name: "checksums.txt"},
		{Name: "checksums.txt.minisig"},
	}
	without := func(name string) []GitHubAsset {
		var out []GitHubAsset
		for _, a := range full {
			if a.Name != name {
				out = append(out, a)
			}
		}
		return out
	}

	assets, err := findReleaseAssets(&GitHubRelease{Assets: full}, "claw-linux-amd64.tar.gz")
	if err != nil {
		t.Fatalf("complete release: %v", err)
	}
	if assets.archive.Name != "claw-linux-amd64.tar.gz" || assets.checksums.Name != "checksums.txt" || assets.signature.Name != "checksums.txt.minisig" {
		t.Errorf("wrong assets picked: %+v", assets)
	}

	for _, missing := range []string{"claw-linux-amd64.tar.gz", "checksums.txt", "checksums.txt.minisig"} {
		if _, err := findReleaseAssets(&GitHubRelease{Assets: without(missing)}, "claw-linux-amd64.tar.gz"); err == nil {
			t.Errorf("release without %s accepted", missing)
		}
	}
}
