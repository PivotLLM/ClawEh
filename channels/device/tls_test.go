// ClawEh
// License: MIT

package device

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/utils"
)

// testTLSConfig is a throwaway self-signed certificate for 127.0.0.1, in the
// shape the gateway's certificate manager hands the channel.
func testTLSConfig(t *testing.T) *tls.Config {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "claw-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS12}
}

// startChannel builds and starts a device channel on a free loopback port and
// stops it when the test ends.
func startChannel(t *testing.T, cfg config.DeviceChannelConfig, tlsConf *tls.Config) (*DeviceChannel, string) {
	t.Helper()
	port := freePort(t)
	cfg.Enabled, cfg.Host, cfg.Port = true, "127.0.0.1", port
	dc, err := NewDeviceChannel(cfg, t.TempDir(), false, bus.NewMessageBus(), "", nil)
	if err != nil {
		t.Fatalf("NewDeviceChannel: %v", err)
	}
	if tlsConf != nil {
		dc.SetTLSConfig(tlsConf)
	}
	if err = dc.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		if err := dc.Stop(context.Background()); err != nil {
			t.Errorf("Stop: %v", err)
		}
	})
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	waitFor(t, "listener", func() bool { return canConnect(addr) })
	return dc, addr
}

// plainGet issues an unencrypted GET and returns the status, or 0 when the
// connection fails.
func plainGet(t *testing.T, addr string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+addr+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		return 0
	}
	status := resp.StatusCode
	if err := resp.Body.Close(); err != nil {
		t.Errorf("closing response body: %v", err)
	}
	return status
}

// TestDeviceListenerTLS: with channels.device.tls the listener completes a
// TLS handshake with the lent certificate and never hands plain HTTP to the
// WebSocket handler; without a certificate to lend, Start refuses rather
// than serving unencrypted.
func TestDeviceListenerTLS(t *testing.T) {
	// No certificate manager (HTTPS was off at start): fail closed.
	bare, err := NewDeviceChannel(config.DeviceChannelConfig{Enabled: true, Host: "127.0.0.1", Port: freePort(t), TLS: true},
		t.TempDir(), false, bus.NewMessageBus(), "", nil)
	if err != nil {
		t.Fatalf("NewDeviceChannel: %v", err)
	}
	if err = bare.Start(context.Background()); !errors.Is(err, errNoCertificate) {
		t.Fatalf("Start without a certificate: err = %v, want errNoCertificate", err)
	}

	_, addr := startChannel(t, config.DeviceChannelConfig{TLS: true}, testTLSConfig(t))

	// A TLS client sees the lent certificate over HTTP/1.1.
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 2 * time.Second}, "tcp", addr,
		&tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatalf("tls dial: %v", err)
	}
	state := conn.ConnectionState()
	if len(state.PeerCertificates) == 0 || state.PeerCertificates[0].Subject.CommonName != "claw-test" {
		t.Errorf("served certificate = %+v, want the lent one", state.PeerCertificates)
	}
	if state.NegotiatedProtocol == "h2" {
		t.Error("h2 negotiated; WebSocket upgrades need HTTP/1.1")
	}
	utils.CloseQuietly(conn)

	// Plain HTTP gets Go's "client sent an HTTP request to an HTTPS server"
	// 400 (or no connection), never the handler's 404.
	if status := plainGet(t, addr); status != 0 && status != http.StatusBadRequest {
		t.Errorf("plain HTTP answered %d on a TLS listener, want 400 or a failed connection", status)
	}
}

// TestDeviceListenerPlainByDefault: tls off keeps the listener as it was.
func TestDeviceListenerPlainByDefault(t *testing.T) {
	_, addr := startChannel(t, config.DeviceChannelConfig{}, nil)
	if status := plainGet(t, addr); status != http.StatusNotFound {
		t.Errorf("non-upgrade GET = %d, want 404", status)
	}
}
