// ClawEh
// License: MIT

package tlscert

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/PivotLLM/ClawEh/config"
)

// infoFrom describes a parsed certificate.
func infoFrom(leaf *x509.Certificate, source Source, certPath, keyPath string) Info {
	return Info{
		Source:      source,
		CertFile:    certPath,
		KeyFile:     keyPath,
		Subject:     leaf.Subject.String(),
		DNSNames:    append([]string(nil), leaf.DNSNames...),
		IPAddresses: ipStrings(leaf.IPAddresses),
		NotBefore:   leaf.NotBefore,
		NotAfter:    leaf.NotAfter,
		Fingerprint: Fingerprint(leaf),
		SelfSigned:  isSelfSigned(leaf),
	}
}

// isSelfSigned reports whether leaf names itself as issuer and its signature
// verifies with its own key.
func isSelfSigned(leaf *x509.Certificate) bool {
	// CheckSignatureFrom would refuse a leaf that is not a CA, which the
	// generated certificate is not, so the signature is checked directly.
	return bytes.Equal(leaf.RawIssuer, leaf.RawSubject) &&
		leaf.CheckSignature(leaf.SignatureAlgorithm, leaf.RawTBSCertificate, leaf.Signature) == nil
}

// Fingerprint is the SHA-256 of the DER certificate as colon-separated upper
// case hex, the form browsers and openssl print.
func Fingerprint(leaf *x509.Certificate) string {
	sum := sha256.Sum256(leaf.Raw)
	h := strings.ToUpper(hex.EncodeToString(sum[:]))
	parts := make([]string, 0, len(h)/2)
	for i := 0; i < len(h); i += 2 {
		parts = append(parts, h[i:i+2])
	}
	return strings.Join(parts, ":")
}

// InspectFile describes the first certificate in a PEM file without needing
// its key. Source is inferred from the options' paths.
func InspectFile(opts Options) (Info, error) {
	certPath, keyPath := opts.Paths()
	data, err := os.ReadFile(certPath) //nolint:gosec // operator-configured certificate path
	if err != nil {
		return Info{}, err
	}
	for {
		var block *pem.Block
		block, data = pem.Decode(data)
		if block == nil {
			return Info{}, fmt.Errorf("%s: no CERTIFICATE block", certPath)
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		leaf, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return Info{}, fmt.Errorf("%s: %w", certPath, err)
		}
		return infoFrom(leaf, opts.Source(), certPath, keyPath), nil
	}
}

// NamesForConfig returns the names of the certificate the gateway serves under
// cfg — for the Host header check and the device gateway's host allowlist. It
// is nil when the HTTPS listener is off or the certificate cannot be read (a
// missing self-signed pair is generated when the listener starts, which
// happens before the channels are built).
func NamesForConfig(cfg *config.Config) []string {
	if cfg == nil || !cfg.Gateway.HTTPSEnabled() {
		return nil
	}
	info, err := InspectFile(OptionsFromConfig(cfg))
	if err != nil {
		return nil
	}
	return info.Names()
}

// errNoCertificate is returned by Manager methods before Load has stored one.
var errNoCertificate = errors.New("tlscert: no certificate loaded")

// ValidatePair checks an operator-supplied PEM pair before it is saved to
// the config, so a WebUI edit cannot point the HTTPS listener at files the
// gateway would refuse to start on. Both paths must be absolute and readable
// by this process (the service user), the key must match the certificate,
// and the certificate must be valid at now. The error names what is wrong.
func ValidatePair(certPath, keyPath string, now time.Time) (Info, error) {
	certPath, keyPath = strings.TrimSpace(certPath), strings.TrimSpace(keyPath)
	switch {
	case certPath == "":
		return Info{}, errors.New("cert_file is required")
	case keyPath == "":
		return Info{}, errors.New("key_file is required")
	case !filepath.IsAbs(certPath):
		return Info{}, fmt.Errorf("cert_file %q must be an absolute path", certPath)
	case !filepath.IsAbs(keyPath):
		return Info{}, fmt.Errorf("key_file %q must be an absolute path", keyPath)
	}
	certPEM, err := os.ReadFile(certPath) //nolint:gosec // operator-chosen certificate path, checked on purpose
	if err != nil {
		return Info{}, fmt.Errorf("cert_file %s cannot be read by the service user: %w", certPath, err)
	}
	keyPEM, err := os.ReadFile(keyPath) //nolint:gosec // operator-chosen key path, checked on purpose
	if err != nil {
		return Info{}, fmt.Errorf("key_file %s cannot be read by the service user: %w", keyPath, err)
	}
	if block, _ := pem.Decode(certPEM); block == nil {
		return Info{}, fmt.Errorf("cert_file %s is not PEM", certPath)
	}
	if block, _ := pem.Decode(keyPEM); block == nil {
		return Info{}, fmt.Errorf("key_file %s is not PEM", keyPath)
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return Info{}, fmt.Errorf("cert_file %s and key_file %s do not form a pair: %w", certPath, keyPath, err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return Info{}, fmt.Errorf("cert_file %s: %w", certPath, err)
	}
	if now.After(leaf.NotAfter) {
		return Info{}, fmt.Errorf("cert_file %s expired %s", certPath, leaf.NotAfter.Format(time.RFC3339))
	}
	if now.Before(leaf.NotBefore) {
		return Info{}, fmt.Errorf("cert_file %s is not valid until %s", certPath, leaf.NotBefore.Format(time.RFC3339))
	}
	return infoFrom(leaf, SourceFile, certPath, keyPath), nil
}
