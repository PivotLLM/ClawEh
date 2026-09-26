// ClawEh
// License: MIT

package tlscert

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"strings"

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
	}
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
