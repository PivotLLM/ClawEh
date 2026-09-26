// ClawEh
// License: MIT

package tlscert

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/internal/testalerts"
)

// writePair writes a self-signed pair for names into dir and returns its paths.
func writePair(t *testing.T, dir string, names certNames, now time.Time) (string, string) {
	t.Helper()
	certPath, keyPath := filepath.Join(dir, "user.crt"), filepath.Join(dir, "user.key")
	if err := writeSelfSigned(certPath, keyPath, names, now); err != nil {
		t.Fatalf("writeSelfSigned: %v", err)
	}
	return certPath, keyPath
}

// touch bumps a file's mtime well past the previous one so the stamp differs
// even on a coarse-grained filesystem.
func touch(t *testing.T, path string) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, time.Now(), fi.ModTime().Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
}

func TestLoad_SelfSignedGeneratesAndReuses(t *testing.T) {
	dir := t.TempDir()
	opts := Options{DataDir: dir, ExtraNames: []string{"alias.example", "203.0.113.7"}, ExternalHost: "claw.example.com"}

	m, err := Load(opts)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	info := m.Info()
	if info.Source != SourceSelfSigned {
		t.Errorf("source = %q, want self-signed", info.Source)
	}
	if info.CertFile != filepath.Join(dir, DirName, SelfSignedCertFile) {
		t.Errorf("cert path = %q", info.CertFile)
	}
	if fi, statErr := os.Stat(info.KeyFile); statErr != nil {
		t.Errorf("stat key: %v", statErr)
	} else if fi.Mode().Perm() != 0o600 {
		t.Errorf("key mode = %v, want 0600", fi.Mode().Perm())
	}
	if fi, statErr := os.Stat(filepath.Join(dir, DirName)); statErr != nil {
		t.Errorf("stat tls dir: %v", statErr)
	} else if fi.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v, want 0700", fi.Mode().Perm())
	}
	names := m.Info().Names()
	if hn, hostErr := os.Hostname(); hostErr == nil && hn != "" && !slices.Contains(names, strings.ToLower(hn)) {
		t.Errorf("names %v lack the host name %q", names, hn)
	}
	for _, want := range []string{"alias.example", "203.0.113.7", "claw.example.com"} {
		if !slices.Contains(names, want) {
			t.Errorf("names %v lack %q", names, want)
		}
	}
	if addrs, ifErr := net.InterfaceAddrs(); ifErr == nil {
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP.IsLoopback() || ipn.IP.IsLinkLocalUnicast() {
				continue
			}
			if !slices.Contains(names, ipn.IP.String()) {
				t.Errorf("names %v lack interface address %s", names, ipn.IP)
			}
		}
	}
	if left := time.Until(info.NotAfter); left < 360*day || left > 366*day {
		t.Errorf("validity %v, want about a year", left)
	}
	if len(info.Fingerprint) != 95 || strings.Count(info.Fingerprint, ":") != 31 {
		t.Errorf("fingerprint %q is not colon-separated SHA-256", info.Fingerprint)
	}

	// Restart: same files, same certificate.
	m2, err := Load(opts)
	if err != nil {
		t.Fatalf("second Load: %v", err)
	}
	if m2.Info().Fingerprint != info.Fingerprint {
		t.Error("a valid self-signed certificate was regenerated on restart")
	}

	// A name the certificate does not cover forces regeneration; dropping a
	// name does not (the old certificate still covers everything wanted).
	opts.ExtraNames = append(opts.ExtraNames, "new.example")
	m3, err := Load(opts)
	if err != nil {
		t.Fatalf("Load with new name: %v", err)
	}
	if m3.Info().Fingerprint == info.Fingerprint || !slices.Contains(m3.Info().Names(), "new.example") {
		t.Errorf("certificate not regenerated for a new name: %v", m3.Info().Names())
	}
	opts.ExtraNames = nil
	m4, err := Load(opts)
	if err != nil {
		t.Fatalf("Load with fewer names: %v", err)
	}
	if m4.Info().Fingerprint != m3.Info().Fingerprint {
		t.Error("certificate regenerated although it covered every name")
	}
}

func TestLoad_SelfSignedRegeneratedNearExpiry(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-(selfSignedValidity - 20*day)) // 20 days left
	opts := Options{DataDir: dir, Now: func() time.Time { return old }}
	m, err := Load(opts)
	if err != nil {
		t.Fatal(err)
	}
	first := m.Info().Fingerprint

	opts.Now = nil
	m2, err := Load(opts)
	if err != nil {
		t.Fatal(err)
	}
	if m2.Info().Fingerprint == first {
		t.Error("certificate with 20 days left was not regenerated")
	}
	if time.Until(m2.Info().NotAfter) < 360*day {
		t.Error("regenerated certificate is not valid for a year")
	}
}

func TestCheckExpiry_SelfSignedRenewsAndUserCertAlerts(t *testing.T) {
	dir := t.TempDir()
	rec := &testalerts.Recorder{}
	// Self-signed, 10 days left at check time: renewed, no alert.
	gen := time.Now().Add(-(selfSignedValidity - 10*day))
	m, err := Load(Options{DataDir: dir, Alerter: rec, Now: func() time.Time { return gen }})
	if err != nil {
		t.Fatal(err)
	}
	first := m.Info().Fingerprint
	m.opts.Now = nil
	var changed []Info
	m.OnChange(func(i Info) { changed = append(changed, i) })
	m.CheckExpiry()
	if m.Info().Fingerprint == first || len(changed) != 1 {
		t.Error("self-signed certificate within 30 days of expiry was not renewed")
	}
	if len(rec.Alerts()) != 0 {
		t.Errorf("unexpected alerts %+v", rec.Alerts())
	}

	// User-supplied, 13 days left: one alert for the 14-day mark, once.
	certPath, keyPath := writePair(t, dir, certNames{dns: []string{"user.example"}}, time.Now().Add(-(selfSignedValidity - 13*day)))
	um, err := Load(Options{CertFile: certPath, KeyFile: keyPath, Alerter: rec})
	if err != nil {
		t.Fatal(err)
	}
	um.CheckExpiry()
	um.CheckExpiry()
	got := rec.Alerts()
	if len(got) != 1 || got[0].Title != "TLS certificate expires soon" || got[0].EventID != "tls-expiry" {
		t.Fatalf("alerts = %+v, want one expiry alert", got)
	}
	// Past the 3-day mark: a second alert.
	um.opts.Now = func() time.Time { return time.Now().Add(11 * day) }
	um.CheckExpiry()
	if len(rec.Alerts()) != 2 {
		t.Errorf("alerts = %d, want the 3-day alert as well", len(rec.Alerts()))
	}
}

func TestLoad_UserPairErrors(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writePair(t, dir, certNames{dns: []string{"a.example"}}, time.Now())
	_, otherKey := writePair(t, filepath.Join(dir, "other"), certNames{dns: []string{"b.example"}}, time.Now())

	if _, err := Load(Options{CertFile: certPath, KeyFile: otherKey}); err == nil {
		t.Error("mismatched key accepted")
	}
	if _, err := Load(Options{CertFile: certPath, KeyFile: filepath.Join(dir, "missing.key")}); err == nil {
		t.Error("missing key accepted")
	}
	expCert, expKey := writePair(t, filepath.Join(dir, "expired"), certNames{dns: []string{"c.example"}}, time.Now().Add(-2*selfSignedValidity))
	if _, err := Load(Options{CertFile: expCert, KeyFile: expKey}); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Errorf("expired certificate accepted or wrong error: %v", err)
	}
	m, err := Load(Options{CertFile: certPath, KeyFile: keyPath})
	if err != nil {
		t.Fatal(err)
	}
	if !m.UserSupplied() || m.Info().Source != SourceFile {
		t.Error("user pair not reported as file source")
	}
	if err := m.Regenerate(); err == nil {
		t.Error("Regenerate must refuse a user-supplied certificate")
	}
}

func TestReload_SwapsOnChangeKeepsOldOnFailure(t *testing.T) {
	dir := t.TempDir()
	rec := &testalerts.Recorder{}
	certPath, keyPath := writePair(t, dir, certNames{dns: []string{"one.example"}}, time.Now())
	m, err := Load(Options{CertFile: certPath, KeyFile: keyPath, Alerter: rec})
	if err != nil {
		t.Fatal(err)
	}
	first := m.Info().Fingerprint
	var seen []Info
	m.OnChange(func(i Info) { seen = append(seen, i) })

	if changed, rerr := m.Reload(); rerr != nil || changed {
		t.Fatalf("unchanged files: changed=%v err=%v", changed, rerr)
	}

	// Replace with a different pair: served certificate follows.
	writePair(t, dir, certNames{dns: []string{"two.example"}}, time.Now())
	touch(t, certPath)
	changed, err := m.Reload()
	if err != nil || !changed {
		t.Fatalf("reload after change: changed=%v err=%v", changed, err)
	}
	if m.Info().Fingerprint == first || !slices.Contains(m.Info().DNSNames, "two.example") || len(seen) != 1 {
		t.Errorf("new certificate not served: %+v", m.Info())
	}
	// The handshake path sees the new leaf.
	c, err := m.TLSConfig().GetCertificate(&tls.ClientHelloInfo{})
	if err != nil || Fingerprint(c.Leaf) != m.Info().Fingerprint {
		t.Errorf("GetCertificate returned a stale certificate: %v", err)
	}
	second := m.Info().Fingerprint

	// Corrupt the key: old certificate stays, one alert, not repeated.
	if err := os.WriteFile(keyPath, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	touch(t, keyPath)
	if changed, err := m.Reload(); err == nil || changed {
		t.Fatalf("broken pair: changed=%v err=%v; want an error and no swap", changed, err)
	}
	// The same broken stamp again is skipped silently: no error, no swap.
	if changed, rerr := m.Reload(); rerr != nil || changed {
		t.Errorf("repeated broken reload: changed=%v err=%v; want it skipped", changed, rerr)
	}
	if m.Info().Fingerprint != second {
		t.Error("broken reload replaced the served certificate")
	}
	got := rec.Alerts()
	if len(got) != 1 || got[0].Title != "TLS certificate reload failed" || got[0].EventID != "tls-reload" {
		t.Errorf("alerts = %+v, want exactly one reload-failed alert", got)
	}
	if len(seen) != 1 {
		t.Errorf("OnChange fired %d times, want 1", len(seen))
	}

	// A good pair after the failure is picked up again.
	writePair(t, dir, certNames{dns: []string{"three.example"}}, time.Now())
	touch(t, certPath)
	touch(t, keyPath)
	if changed, err := m.Reload(); err != nil || !changed {
		t.Fatalf("recovery reload: changed=%v err=%v", changed, err)
	}
	if !slices.Contains(m.Info().DNSNames, "three.example") {
		t.Errorf("recovered certificate not served: %v", m.Info().DNSNames)
	}
}

func TestTLSConfig_MinVersionAndServes(t *testing.T) {
	m, err := Load(Options{DataDir: t.TempDir(), ExtraNames: []string{"srv.example"}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := m.TLSConfig()
	if cfg.MinVersion != tls.VersionTLS12 {
		t.Errorf("MinVersion = %x, want TLS 1.2", cfg.MinVersion)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := ln.Close(); closeErr != nil {
			t.Errorf("close listener: %v", closeErr)
		}
	}()
	// The server side reports through the channel so the test can wait for it
	// instead of racing its Close.
	served := make(chan error, 1)
	go func() {
		c, acceptErr := ln.Accept()
		if acceptErr != nil {
			served <- acceptErr
			return
		}
		tc, ok := c.(*tls.Conn)
		if !ok {
			served <- fmt.Errorf("accepted %T, want *tls.Conn", c)
			return
		}
		hsErr := tc.Handshake()
		if closeErr := c.Close(); hsErr == nil {
			hsErr = closeErr
		}
		served <- hsErr
	}()
	pool := certPool(t, m.Info().CertFile)
	conn, err := tls.Dial("tcp", ln.Addr().String(), &tls.Config{RootCAs: pool, ServerName: "srv.example", MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if err = conn.Close(); err != nil {
		t.Errorf("close client: %v", err)
	}
	if err = <-served; err != nil {
		t.Errorf("server side: %v", err)
	}
}

func TestInspectFileAndNamesForConfig(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writePair(t, dir, certNames{dns: []string{"insp.example"}, ips: []net.IP{net.ParseIP("192.0.2.9")}}, time.Now())
	info, err := InspectFile(Options{CertFile: certPath, KeyFile: keyPath})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"insp.example", "192.0.2.9"}; !slices.Equal(info.Names(), want) {
		t.Errorf("names = %v, want %v", info.Names(), want)
	}
	if info.Subject != "CN=insp.example" {
		t.Errorf("subject = %q", info.Subject)
	}
	if _, err := InspectFile(Options{CertFile: keyPath, KeyFile: keyPath}); err == nil {
		t.Error("a key file must not inspect as a certificate")
	}
}

func TestExpectedNames_Dedupes(t *testing.T) {
	n := expectedNames(Options{ExtraNames: []string{"A.Example", "a.example", "[2001:db8::1]", "2001:db8::1", "localhost", "127.0.0.1"}, ExternalHost: "a.example"})
	if c := countOf(n.dns, "a.example"); c != 1 {
		t.Errorf("a.example appears %d times in %v", c, n.dns)
	}
	if slices.Contains(n.dns, "localhost") {
		t.Errorf("loopback names must be skipped: %v", n.dns)
	}
	ips := ipStrings(n.ips)
	if countOf(ips, "2001:db8::1") != 1 || slices.Contains(ips, "127.0.0.1") {
		t.Errorf("ips = %v", ips)
	}
}

// certPool trusts the certificate at path, so a client can verify a
// self-signed server the way a browser does after the operator accepts it.
func certPool(t *testing.T, path string) *x509.CertPool {
	t.Helper()
	pemBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		t.Fatalf("%s: no certificate", path)
	}
	return pool
}

func countOf(list []string, s string) int {
	n := 0
	for _, v := range list {
		if v == s {
			n++
		}
	}
	return n
}
