// ClawEh
// License: MIT

package tlscert

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/utils"
)

// fqdnLookupTimeout bounds the DNS lookup for the host's canonical name so a
// resolver that is down cannot hold up gateway start.
const fqdnLookupTimeout = 2 * time.Second

// certNames is the name set a self-signed certificate is issued for.
type certNames struct {
	dns []string
	ips []net.IP
}

// expectedNames is what the self-signed certificate must cover today: the
// host name, its FQDN when resolvable, every non-loopback interface address,
// the host of external_url and the configured extra names.
func expectedNames(opts Options) certNames {
	var n certNames
	if hostname, err := os.Hostname(); err == nil {
		hostname = strings.TrimSpace(hostname)
		if hostname != "" {
			n.add(hostname)
			if fqdn := lookupFQDN(hostname); fqdn != "" {
				n.add(fqdn)
			}
		}
	}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP.IsLoopback() || ipn.IP.IsLinkLocalUnicast() || ipn.IP.IsUnspecified() {
				continue
			}
			n.addIP(ipn.IP)
		}
	}
	n.add(opts.ExternalHost)
	for _, extra := range opts.ExtraNames {
		n.add(extra)
	}
	return n
}

// add files name as an IP or a DNS name; empty and loopback names are skipped
// (the HTTPS listener is never loopback-only, and browsers accept loopback
// without a certificate name anyway).
func (n *certNames) add(name string) {
	name = strings.ToLower(strings.TrimSpace(strings.Trim(name, "[]")))
	if name == "" || name == "localhost" {
		return
	}
	if ip := net.ParseIP(name); ip != nil {
		if !ip.IsLoopback() {
			n.addIP(ip)
		}
		return
	}
	if !slices.Contains(n.dns, name) {
		n.dns = append(n.dns, name)
	}
}

func (n *certNames) addIP(ip net.IP) {
	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
	}
	for _, have := range n.ips {
		if have.Equal(ip) {
			return
		}
	}
	n.ips = append(n.ips, ip)
}

// covers reports whether cert is valid for every name in want.
func (n *certNames) coveredBy(cert *x509.Certificate) bool {
	for _, d := range n.dns {
		if cert.VerifyHostname(d) != nil {
			return false
		}
	}
	for _, ip := range n.ips {
		if cert.VerifyHostname(ip.String()) != nil {
			return false
		}
	}
	return true
}

// lookupFQDN returns the host's canonical name when the resolver knows one
// that is more than the bare host name, else "".
func lookupFQDN(hostname string) string {
	ctx, cancel := context.WithTimeout(context.Background(), fqdnLookupTimeout)
	defer cancel()
	cname, err := net.DefaultResolver.LookupCNAME(ctx, hostname)
	if err != nil {
		return ""
	}
	cname = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(cname), "."))
	if cname == "" || cname == strings.ToLower(hostname) || !strings.Contains(cname, ".") || cname == "localhost" {
		return ""
	}
	return cname
}

// ensureSelfSigned returns the self-signed pair, generating it when force is
// set, when it is missing or unloadable, when it expires within renewBefore,
// or when it no longer covers every expected name. Callers hold m.mu or are
// in Load.
func (m *Manager) ensureSelfSigned(force bool) (*loaded, error) {
	certPath, keyPath := m.opts.Paths()
	now := m.opts.now()
	names := expectedNames(m.opts)
	reason := "forced"
	if !force {
		l, err := loadPair(certPath, keyPath, SourceSelfSigned, now)
		switch {
		case err != nil && !errors.Is(err, fs.ErrNotExist):
			reason = "unloadable: " + err.Error()
		case err != nil:
			reason = "missing"
		case l.info.NotAfter.Sub(now) < renewBefore:
			reason = "expires " + l.info.NotAfter.Format(time.RFC3339)
		case !names.coveredBy(l.cert.Leaf):
			reason = "names changed"
		default:
			return l, nil
		}
	}
	if err := writeSelfSigned(certPath, keyPath, names, now); err != nil {
		return nil, fmt.Errorf("generate self-signed certificate: %w", err)
	}
	l, err := loadPair(certPath, keyPath, SourceSelfSigned, now)
	if err != nil {
		return nil, fmt.Errorf("self-signed certificate just written does not load: %w", err)
	}
	logger.InfoCF("tls", "Self-signed TLS certificate generated", map[string]any{
		"reason": reason, "cert_file": certPath, "names": l.info.Names(),
		"not_after": l.info.NotAfter.Format(time.RFC3339), "fingerprint": l.info.Fingerprint,
	})
	return l, nil
}

// writeSelfSigned generates an ECDSA P-256 key and a one-year self-signed
// certificate for names and writes them: the directory 0700, the key 0600,
// the certificate 0644, each file replaced atomically.
func writeSelfSigned(certPath, keyPath string, names certNames, now time.Time) error {
	certPEM, keyPEM, err := generateSelfSigned(names, now)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(certPath), 0o700); err != nil {
		return err
	}
	if err := writeAtomic(keyPath, keyPEM, 0o600); err != nil {
		return err
	}
	return writeAtomic(certPath, certPEM, 0o644) // the certificate is public
}

// generateSelfSigned builds the PEM pair. The subject CN is the first DNS
// name (the host name) so `claw tls` and browsers show something meaningful.
func generateSelfSigned(names certNames, now time.Time) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, nil, err
	}
	cn := "claw"
	if len(names.dns) > 0 {
		cn = names.dns[0]
	} else if len(names.ips) > 0 {
		cn = names.ips[0].String()
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.Add(selfSignedValidity),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              names.dns,
		IPAddresses:           names.ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}

// writeAtomic writes data to a temp file beside path with mode and renames it
// into place, so a watcher never sees a half-written file.
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	cleanup := func() {
		if rmErr := os.Remove(name); rmErr != nil {
			logger.WarnCF("tls", "could not remove temp file", map[string]any{"path": name, "error": rmErr.Error()})
		}
	}
	if _, err := tmp.Write(data); err != nil {
		utils.CloseQuietly(tmp)
		cleanup()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		utils.CloseQuietly(tmp)
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(name, path); err != nil {
		cleanup()
		return err
	}
	return nil
}
