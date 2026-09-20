package upgrade

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestCompareSemVer(t *testing.T) {
	cases := []struct {
		v1, v2 string
		want   int
	}{
		{"0.5.3", "0.5.3", 0},
		{"v0.5.3", "0.5.3", 0},
		{"0.5.3", "v0.5.4", -1},
		{"0.5.4", "0.5.3", 1},
		{"0.5.10", "0.5.9", 1},
		{"0.4.99", "0.5.0", -1},
		{"1.0.0", "0.9.9", 1},
		{"0.5.3+27691883", "0.5.3", 0},
		{"0.5.3+27691883", "0.5.4", -1},
	}

	for _, tc := range cases {
		got := compareSemVer(tc.v1, tc.v2)
		if got != tc.want {
			t.Errorf("compareSemVer(%q, %q) = %d, want %d", tc.v1, tc.v2, got, tc.want)
		}
	}
}

func TestReadExpectedChecksum(t *testing.T) {
	dir := t.TempDir()
	checksumFile := filepath.Join(dir, "test.sha256")
	content := "a52739a41368d1afbb8339d638d0c3722e2e576888bb4dac320e60a8f03ec20f  claw-linux-amd64.tar.gz\n"
	if err := os.WriteFile(checksumFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := readExpectedChecksum(checksumFile)
	if err != nil {
		t.Fatalf("readExpectedChecksum error = %v", err)
	}
	want := "a52739a41368d1afbb8339d638d0c3722e2e576888bb4dac320e60a8f03ec20f"
	if got != want {
		t.Errorf("readExpectedChecksum = %q, want %q", got, want)
	}
}

func TestComputeSHA256(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "data.bin")
	data := []byte("hello world claw upgrade")
	if err := os.WriteFile(file, data, 0o644); err != nil {
		t.Fatal(err)
	}

	h := sha256.Sum256(data)
	want := hex.EncodeToString(h[:])

	got, err := computeSHA256(file)
	if err != nil {
		t.Fatalf("computeSHA256 error = %v", err)
	}
	if got != want {
		t.Errorf("computeSHA256 = %q, want %q", got, want)
	}
}

func TestExtractBinariesFromTarGz(t *testing.T) {
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "test.tar.gz")

	// Create test tar.gz with `claw` and `claw-auth`
	f, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)

	clawContent := []byte("#!/bin/sh\necho claw\n")
	hdr := &tar.Header{
		Name:     "claw",
		Mode:     0o755,
		Size:     int64(len(clawContent)),
		Typeflag: tar.TypeReg,
	}
	if hdrErr := tw.WriteHeader(hdr); hdrErr != nil {
		t.Fatal(hdrErr)
	}
	if _, wErr := tw.Write(clawContent); wErr != nil {
		t.Fatal(wErr)
	}

	authContent := []byte("#!/bin/sh\necho auth\n")
	hdrAuth := &tar.Header{
		Name:     "claw-auth",
		Mode:     0o755,
		Size:     int64(len(authContent)),
		Typeflag: tar.TypeReg,
	}
	if hdrErr := tw.WriteHeader(hdrAuth); hdrErr != nil {
		t.Fatal(hdrErr)
	}
	if _, wErr := tw.Write(authContent); wErr != nil {
		t.Fatal(wErr)
	}

	if closeErr := tw.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if closeErr := gz.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if closeErr := f.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}

	destClaw := filepath.Join(dir, "extracted-claw")
	destAuth := filepath.Join(dir, "extracted-auth")

	hasAuth, err := extractBinariesFromTarGz(archivePath, destClaw, destAuth)
	if err != nil {
		t.Fatalf("extractBinariesFromTarGz error = %v", err)
	}
	if !hasAuth {
		t.Error("expected hasAuth to be true")
	}

	extractedClawData, err := os.ReadFile(destClaw)
	if err != nil || string(extractedClawData) != string(clawContent) {
		t.Errorf("extracted claw mismatch: got %q, want %q", string(extractedClawData), string(clawContent))
	}

	extractedAuthData, err := os.ReadFile(destAuth)
	if err != nil || string(extractedAuthData) != string(authContent) {
		t.Errorf("extracted auth mismatch: got %q, want %q", string(extractedAuthData), string(authContent))
	}
}
