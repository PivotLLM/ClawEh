// ClawEh
// License: MIT

// Package tlscert provides the certificate behind the gateway's HTTPS listener:
// either the operator's PEM pair (gateway.tls.cert_file/key_file) or a
// self-signed certificate the package generates and renews under
// <CLAW_HOME>/tls. Whatever the source, the files are watched and a changed
// pair is swapped in behind tls.Config.GetCertificate without restarting the
// listener; a pair that fails to load leaves the previous certificate serving
// and raises an alert.
package tlscert

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tenebris-tech/alerter"

	"github.com/PivotLLM/ClawEh/alerts"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/logger"
)

// Source says where the certificate comes from.
type Source string

const (
	// SourceFile is an operator-supplied PEM pair.
	SourceFile Source = "file"
	// SourceSelfSigned is the pair this package generates under <CLAW_HOME>/tls.
	SourceSelfSigned Source = "self-signed"
)

const (
	// DirName is the directory under CLAW_HOME that holds the self-signed pair.
	DirName = "tls"
	// SelfSignedCertFile and SelfSignedKeyFile are the self-signed pair's names.
	SelfSignedCertFile = "self-signed.crt"
	SelfSignedKeyFile  = "self-signed.key"

	// DefaultPollInterval is how often Watch re-stats the certificate files.
	DefaultPollInterval = 60 * time.Second
	// expiryCheckInterval is how often Watch re-checks the expiry date.
	expiryCheckInterval = 24 * time.Hour

	// selfSignedValidity is how long a generated certificate lasts, and
	// renewBefore how close to expiry it is regenerated.
	selfSignedValidity = 365 * 24 * time.Hour
	renewBefore        = 30 * 24 * time.Hour

	day = 24 * time.Hour
)

// expiryWarnDays are the days-before-expiry at which a user-supplied
// certificate raises an alert, once each; 0 is "it has expired".
var expiryWarnDays = []int{14, 3, 0}

// Options selects and describes the certificate.
type Options struct {
	// DataDir is CLAW_HOME; the self-signed pair lives in DataDir/tls.
	DataDir string
	// CertFile and KeyFile, both set, name the operator's PEM pair.
	CertFile, KeyFile string
	// ExtraNames are additional DNS names or IPs for the self-signed certificate.
	ExtraNames []string
	// ExternalHost is the host of gateway.external_url, added to the
	// self-signed certificate's names when set.
	ExternalHost string
	// Alerter receives reload and expiry alerts; nil uses the process default.
	Alerter alerter.Alerter
	// Now is the clock; nil means time.Now. Tests use it to age certificates.
	Now func() time.Time
}

// OptionsFromConfig maps the gateway config onto Options.
func OptionsFromConfig(cfg *config.Config) Options {
	o := Options{
		DataDir:    cfg.DataDir(),
		CertFile:   strings.TrimSpace(cfg.Gateway.TLS.CertFile),
		KeyFile:    strings.TrimSpace(cfg.Gateway.TLS.KeyFile),
		ExtraNames: cfg.Gateway.TLS.ExtraNames,
	}
	if u, err := url.Parse(cfg.Gateway.ExternalURL); err == nil {
		o.ExternalHost = u.Hostname()
	}
	return o
}

// Source is where the certificate comes from under these options.
func (o Options) Source() Source {
	if o.CertFile != "" && o.KeyFile != "" {
		return SourceFile
	}
	return SourceSelfSigned
}

// Paths are the certificate and key files in use.
func (o Options) Paths() (cert, key string) {
	if o.Source() == SourceFile {
		return o.CertFile, o.KeyFile
	}
	dir := filepath.Join(o.DataDir, DirName)
	return filepath.Join(dir, SelfSignedCertFile), filepath.Join(dir, SelfSignedKeyFile)
}

func (o Options) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

// Info describes a loaded certificate, for logs, the report and `claw tls`.
type Info struct {
	Source            Source
	CertFile, KeyFile string
	Subject           string
	DNSNames          []string
	IPAddresses       []string
	NotBefore         time.Time
	NotAfter          time.Time
	// Fingerprint is the SHA-256 of the DER certificate, colon-separated.
	Fingerprint string
}

// Names are every DNS name and IP address the certificate is valid for, in
// the form the Host header check and the device gateway take.
func (i Info) Names() []string {
	out := make([]string, 0, len(i.DNSNames)+len(i.IPAddresses))
	out = append(out, i.DNSNames...)
	return append(out, i.IPAddresses...)
}

// loaded is one certificate as served, with the file state it came from.
type loaded struct {
	cert  *tls.Certificate
	info  Info
	stamp fileStamp
}

// Manager holds the certificate the HTTPS listener serves and keeps it
// current. All methods are safe for concurrent use; the TLS handshake path
// (GetCertificate) is a single atomic load.
type Manager struct {
	opts    Options
	current atomic.Pointer[loaded]

	mu         sync.Mutex // serialises Reload, Regenerate and CheckExpiry
	lastFailed *fileStamp // the file state of the last failed reload, reported once
	warned     map[int]bool
	onChange   atomic.Pointer[func(Info)]
}

// Load prepares the certificate: a user pair is loaded and checked; a
// self-signed pair is reused when present, valid for another 30 days and
// covering every current name, otherwise (re)generated.
func Load(opts Options) (*Manager, error) {
	m := &Manager{opts: opts, warned: map[int]bool{}}
	if opts.Source() == SourceFile {
		l, err := loadPair(opts.CertFile, opts.KeyFile, SourceFile, opts.now())
		if err != nil {
			return nil, fmt.Errorf("gateway.tls: %w", err)
		}
		m.current.Store(l)
		return m, nil
	}
	l, err := m.ensureSelfSigned(false)
	if err != nil {
		return nil, err
	}
	m.current.Store(l)
	return m, nil
}

// Info describes the certificate currently served.
func (m *Manager) Info() Info { return m.current.Load().info }

// Source is where the certificate comes from.
func (m *Manager) Source() Source { return m.opts.Source() }

// UserSupplied reports whether the operator provides the certificate. Only
// then may the listener promise HSTS: a self-signed certificate is accepted by
// hand, and an HSTS pin would lock the browser out after a regeneration.
func (m *Manager) UserSupplied() bool { return m.Source() == SourceFile }

// TLSConfig is the listener's TLS configuration: TLS 1.2 or later, Go's
// default cipher suites, and the current certificate on every handshake.
func (m *Manager) TLSConfig() *tls.Config {
	return &tls.Config{
		MinVersion:     tls.VersionTLS12,
		GetCertificate: m.getCertificate,
	}
}

func (m *Manager) getCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	if l := m.current.Load(); l != nil {
		return l.cert, nil
	}
	return nil, errNoCertificate
}

// OnChange registers fn to run, on the watcher's goroutine, whenever a
// different certificate starts being served (reload or regeneration).
func (m *Manager) OnChange(fn func(Info)) {
	m.onChange.Store(&fn)
}

func (m *Manager) swap(l *loaded, why string) {
	m.current.Store(l)
	m.lastFailed = nil
	m.warned = map[int]bool{}
	logger.InfoCF("tls", "TLS certificate "+why, map[string]any{
		"source":      string(l.info.Source),
		"cert_file":   l.info.CertFile,
		"names":       l.info.Names(),
		"not_after":   l.info.NotAfter.Format(time.RFC3339),
		"fingerprint": l.info.Fingerprint,
	})
	if fn := m.onChange.Load(); fn != nil && *fn != nil {
		(*fn)(l.info)
	}
}

// Reload re-stats the certificate files and, when they changed since the
// served pair was loaded, loads and swaps in the new pair. A pair that does
// not parse, whose key does not match, or that has expired is rejected: the
// previous certificate keeps serving and an alert is raised, once per broken
// file state. It reports whether the served certificate changed.
func (m *Manager) Reload() (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur := m.current.Load()
	certPath, keyPath := m.opts.Paths()
	st, err := stampFiles(certPath, keyPath)
	if err == nil && st == cur.stamp {
		return false, nil
	}
	if m.lastFailed != nil && err == nil && st == *m.lastFailed {
		return false, nil // already reported
	}
	var l *loaded
	if err == nil {
		l, err = loadPair(certPath, keyPath, m.opts.Source(), m.opts.now())
	}
	if err != nil {
		m.rememberFailure(st, err)
		return false, err
	}
	m.swap(l, "reloaded from disk")
	return true, nil
}

// rememberFailure records a broken file state so it is alerted once, and
// raises the alert.
func (m *Manager) rememberFailure(st fileStamp, cause error) {
	failed := st
	m.lastFailed = &failed
	certPath, keyPath := m.opts.Paths()
	logger.ErrorCF("tls", "TLS certificate reload failed; keeping the previous certificate", map[string]any{
		"cert_file": certPath, "key_file": keyPath, "error": cause.Error(),
	})
	m.alert(alerter.Alert{
		Title:       "TLS certificate reload failed",
		Description: certPath + " changed on disk but could not be loaded; the previous certificate is still being served",
		Details:     cause.Error(),
		EventID:     "tls-reload",
	})
}

// Regenerate replaces the self-signed pair now, whatever its state, and serves
// the new certificate. It is an error with a user-supplied certificate.
func (m *Manager) Regenerate() error {
	if m.Source() != SourceSelfSigned {
		return errors.New("tlscert: gateway.tls.cert_file/key_file are set; only the self-signed certificate can be regenerated")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	l, err := m.ensureSelfSigned(true)
	if err != nil {
		return err
	}
	m.swap(l, "regenerated")
	return nil
}

// CheckExpiry is the daily check. A user-supplied certificate raises an alert
// 14 and 3 days before it expires and once it has, each once per loaded pair;
// a self-signed certificate within 30 days of expiry is regenerated.
func (m *Manager) CheckExpiry() {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur := m.current.Load()
	left := cur.info.NotAfter.Sub(m.opts.now())
	if m.Source() == SourceSelfSigned {
		if left > renewBefore {
			return
		}
		l, err := m.ensureSelfSigned(true)
		if err != nil {
			logger.ErrorCF("tls", "Self-signed TLS certificate renewal failed", map[string]any{"error": err.Error()})
			m.alert(alerter.Alert{
				Title:       "Self-signed TLS certificate renewal failed",
				Description: "the certificate expires " + cur.info.NotAfter.Format(time.RFC3339) + " and could not be regenerated; HTTPS clients will start refusing it",
				Details:     err.Error(),
				EventID:     "tls-selfsigned",
			})
			return
		}
		m.swap(l, "renewed")
		return
	}
	for _, days := range expiryWarnDays {
		if left > time.Duration(days)*day || m.warned[days] {
			continue
		}
		m.warned[days] = true
		desc := fmt.Sprintf("%s expires %s (in %d days); install a renewed pair — it is picked up within a minute, no restart needed",
			cur.info.CertFile, cur.info.NotAfter.Format(time.RFC3339), int(left/day))
		if days == 0 {
			desc = cur.info.CertFile + " expired " + cur.info.NotAfter.Format(time.RFC3339) + "; browsers now refuse the WebUI until a renewed pair is installed"
		}
		m.alert(alerter.Alert{
			Title:       "TLS certificate expires soon",
			Description: desc,
			EventID:     "tls-expiry",
		})
		return
	}
}

// Watch polls the certificate files every interval (DefaultPollInterval when
// zero) and runs the expiry check now and daily, until ctx ends. It returns
// immediately.
func (m *Manager) Watch(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = DefaultPollInterval
	}
	go func() {
		m.CheckExpiry() //nolint:contextcheck // the FQDN lookup inside is bounded by its own timeout; CheckExpiry is also reached from Load and `claw tls`, which have no context
		poll := time.NewTicker(interval)
		defer poll.Stop()
		daily := time.NewTicker(expiryCheckInterval)
		defer daily.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-poll.C:
				m.Reload() //nolint:errcheck // logged and alerted inside
			case <-daily.C:
				m.CheckExpiry() //nolint:contextcheck // as above
			}
		}
	}()
}

func (m *Manager) alert(a alerter.Alert) {
	if m.opts.Alerter != nil {
		m.opts.Alerter.Send(a)
		return
	}
	alerts.Send(a)
}

// fileStamp is what Watch compares to notice a changed pair: modification
// time, size and, for a symlink (the layout certbot and friends produce),
// the resolved target.
type fileStamp struct {
	certMod, keyMod       time.Time
	certSize, keySize     int64
	certTarget, keyTarget string
}

func stampFiles(certPath, keyPath string) (fileStamp, error) {
	var st fileStamp
	var err error
	if st.certMod, st.certSize, st.certTarget, err = stampFile(certPath); err != nil {
		return fileStamp{}, err
	}
	if st.keyMod, st.keySize, st.keyTarget, err = stampFile(keyPath); err != nil {
		return fileStamp{}, err
	}
	return st, nil
}

func stampFile(path string) (time.Time, int64, string, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return time.Time{}, 0, "", err
	}
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return time.Time{}, 0, "", err
	}
	return fi.ModTime(), fi.Size(), target, nil
}

// loadPair reads and checks a PEM pair: it must parse, the key must match the
// certificate, and the certificate must not have expired.
func loadPair(certPath, keyPath string, source Source, now time.Time) (*loaded, error) {
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("load %s / %s: %w", certPath, keyPath, err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", certPath, err)
	}
	cert.Leaf = leaf
	if now.After(leaf.NotAfter) {
		return nil, fmt.Errorf("%s expired %s", certPath, leaf.NotAfter.Format(time.RFC3339))
	}
	st, err := stampFiles(certPath, keyPath)
	if err != nil {
		return nil, err
	}
	return &loaded{cert: &cert, info: infoFrom(leaf, source, certPath, keyPath), stamp: st}, nil
}

// ipStrings renders IPs for names lists and the report.
func ipStrings(ips []net.IP) []string {
	out := make([]string, 0, len(ips))
	for _, ip := range ips {
		out = append(out, ip.String())
	}
	return out
}
