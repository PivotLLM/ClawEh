package gateway

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/channels"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/internal/tlscert"
	"github.com/PivotLLM/ClawEh/utils"
)

// startHost builds and starts an httpHost whose mux answers body on /ping,
// stopping it when the test ends.
func startHost(t *testing.T, opts hostOptions, body string) *httpHost {
	t.Helper()
	host, err := newHTTPHost(opts)
	if err != nil {
		t.Fatalf("newHTTPHost: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(w http.ResponseWriter, _ *http.Request) {
		if _, writeErr := w.Write([]byte(body)); writeErr != nil {
			t.Errorf("write: %v", writeErr)
		}
	})
	host.SetMux(mux)
	if err := host.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if stopErr := host.Stop(shutdownCtx); stopErr != nil {
			t.Errorf("host.Stop: %v", stopErr)
		}
	})
	waitForServer(t, host.LoopbackAddr(), 2*time.Second)
	return host
}

// selfSignedTLS loads a self-signed certificate for the test's temp dir.
func selfSignedTLS(t *testing.T) *tlscert.Manager {
	t.Helper()
	m, err := tlscert.Load(tlscert.Options{DataDir: t.TempDir(), ExtraNames: []string{"gw.test"}})
	if err != nil {
		t.Fatalf("tlscert.Load: %v", err)
	}
	return m
}

// tlsClient trusts m's certificate for the name gw.test, as a browser does
// after the operator accepts the certificate.
func tlsClient(t *testing.T, m *tlscert.Manager) *http.Client {
	t.Helper()
	pemBytes, err := os.ReadFile(m.Info().CertFile)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		t.Fatal("no certificate in PEM")
	}
	return &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "gw.test", MinVersion: tls.VersionTLS12},
	}}
}

// reply is what a test needs from a response once its body has been read
// and closed.
type reply struct {
	status int
	header http.Header
	tls    *tls.ConnectionState
	body   string
}

// fetch GETs url with c and returns the response with its body consumed and
// closed; a transport error fails the test.
func fetch(t *testing.T, c *http.Client, url string) reply {
	t.Helper()
	resp, err := c.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	return drain(t, resp)
}

// drain reads and closes resp's body.
func drain(t *testing.T, resp *http.Response) reply {
	t.Helper()
	body, err := io.ReadAll(resp.Body)
	if closeErr := resp.Body.Close(); closeErr != nil {
		t.Errorf("close body: %v", closeErr)
	}
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return reply{status: resp.StatusCode, header: resp.Header, tls: resp.TLS, body: string(body)}
}

// TestHTTPHostLifecycleIndependentOfChannelManager asserts that the shared
// HTTP listener (owned by httpHost) is not torn down by the lifecycle of a
// channels.Manager. This is the load-bearing invariant from investigation
// 7a5377d9 / option #1: a config reload must rebuild the channel manager
// without bouncing the listener that backs WebUI WebSockets, the API, and
// channel webhooks.
func TestHTTPHostLifecycleIndependentOfChannelManager(t *testing.T) {
	host := startHost(t, hostOptions{}, "pong")

	// Independently construct + stop a channels.Manager. It must not affect
	// the listener owned by the gateway-level httpHost.
	cfg := config.DefaultConfig()
	cm, err := channels.NewManager(cfg, bus.NewMessageBus(), nil)
	if err != nil {
		t.Fatalf("channels.NewManager: %v", err)
	}
	if err := cm.StopAll(context.Background()); err != nil {
		t.Fatalf("cm.StopAll returned error: %v", err)
	}

	// Listener must still serve traffic after the channel manager is gone.
	if got := httpGetBody(t, "http://"+host.LoopbackAddr()+"/ping"); got != "pong" {
		t.Fatalf("GET /ping after ChannelManager.StopAll: body = %q, want %q (listener was torn down — option #1 regression)", got, "pong")
	}
}

// TestHTTPHostSwapMux verifies SetMux installs a new handler atomically
// without disturbing the listener — the in-flight invariant for the reload
// path. After swap, requests are served by the new mux.
func TestHTTPHostSwapMux(t *testing.T) {
	host := startHost(t, hostOptions{}, "first")
	url := "http://" + host.LoopbackAddr() + "/ping"
	if got := httpGetBody(t, url); got != "first" {
		t.Fatalf("before swap: got %q, want %q", got, "first")
	}

	second := http.NewServeMux()
	second.HandleFunc("/ping", func(w http.ResponseWriter, _ *http.Request) {
		if _, writeErr := w.Write([]byte("second")); writeErr != nil {
			t.Errorf("write: %v", writeErr)
		}
	})
	host.SetMux(second)

	if got := httpGetBody(t, url); got != "second" {
		t.Fatalf("after swap: got %q, want %q (mux swap did not take effect)", got, "second")
	}
}

// TestHTTPHostLoopbackOnly: with no TLS host there is no HTTPS listener, and
// plain HTTP is served on both loopback addresses (IPv6 when the host has it).
func TestHTTPHostLoopbackOnly(t *testing.T) {
	host := startHost(t, hostOptions{}, "pong")
	if host.HTTPSAddr() != "" {
		t.Fatalf("HTTPS listener bound at %s on a loopback-only gateway", host.HTTPSAddr())
	}
	if host.tlsServer != nil {
		t.Fatal("tlsServer must be nil without a TLS host")
	}
	v4, port, err := net.SplitHostPort(host.LoopbackAddr())
	if err != nil || v4 != "127.0.0.1" {
		t.Fatalf("loopback addr = %q", host.LoopbackAddr())
	}
	if got := httpGetBody(t, "http://127.0.0.1:"+port+"/ping"); got != "pong" {
		t.Errorf("IPv4 loopback: %q", got)
	}
	if ln, err := net.Listen("tcp", "[::1]:0"); err == nil { // host has IPv6 loopback
		utils.CloseQuietly(ln)
		if got := httpGetBody(t, "http://[::1]:"+port+"/ping"); got != "pong" {
			t.Errorf("IPv6 loopback: %q", got)
		}
	}
}

// TestHTTPHostHTTPSListener: with a TLS host the same mux is served over TLS
// on a second port; plain HTTP is refused there; loopback stays plain HTTP.
func TestHTTPHostHTTPSListener(t *testing.T) {
	m := selfSignedTLS(t)
	host := startHost(t, hostOptions{TLSHost: "127.0.0.1", TLSConfig: m.TLSConfig()}, "secure")
	if host.HTTPSAddr() == "" {
		t.Fatal("no HTTPS listener bound")
	}
	if got := httpGetBody(t, "http://"+host.LoopbackAddr()+"/ping"); got != "secure" {
		t.Errorf("loopback HTTP: %q", got)
	}

	client := tlsClient(t, m)
	resp := fetch(t, client, "https://"+host.HTTPSAddr()+"/ping")
	if resp.status != http.StatusOK || resp.body != "secure" {
		t.Errorf("HTTPS: status %d body %q", resp.status, resp.body)
	}
	if resp.tls == nil {
		t.Fatal("no TLS state on the HTTPS response")
	}
	if resp.tls.Version < tls.VersionTLS12 {
		t.Errorf("TLS version %x", resp.tls.Version)
	}
	if got := resp.header.Get("Strict-Transport-Security"); got != "" {
		t.Errorf("HSTS %q sent with a self-signed certificate", got)
	}
	if got := resp.header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("security headers missing on HTTPS: %q", got)
	}

	// A plain-HTTP client on the HTTPS port gets no page: Go's TLS server
	// answers a bare 400 ("Client sent an HTTP request to an HTTPS server").
	if plainResp, err := (&http.Client{Timeout: 2 * time.Second}).Get("http://" + host.HTTPSAddr() + "/ping"); err == nil {
		plain := drain(t, plainResp)
		if plain.status != http.StatusBadRequest || plain.body == "secure" {
			t.Errorf("plain HTTP on the HTTPS port: status %d body %q", plain.status, plain.body)
		}
	}
	// An unverified client is refused by TLS itself.
	if unverified, err := (&http.Client{Transport: &http.Transport{}}).Get("https://" + host.HTTPSAddr() + "/ping"); err == nil {
		drain(t, unverified)
		t.Error("a client that does not trust the certificate connected")
	}
}

// TestHTTPHostHSTSOnlyWithUserCertificate: the HSTS header is sent on HTTPS
// responses when the host is told the certificate is operator-supplied, and
// never on the loopback HTTP listener.
func TestHTTPHostHSTSOnlyWithUserCertificate(t *testing.T) {
	m := selfSignedTLS(t)
	host := startHost(t, hostOptions{TLSHost: "127.0.0.1", TLSConfig: m.TLSConfig(), HSTS: true}, "ok")
	resp := fetch(t, tlsClient(t, m), "https://"+host.HTTPSAddr()+"/ping")
	if got := resp.header.Get("Strict-Transport-Security"); got != hstsHeader {
		t.Errorf("HTTPS HSTS = %q, want %q", got, hstsHeader)
	}
	plain := fetch(t, http.DefaultClient, "http://"+host.LoopbackAddr()+"/ping")
	if got := plain.header.Get("Strict-Transport-Security"); got != "" {
		t.Errorf("HSTS %q sent on plain HTTP", got)
	}
}

// TestHTTPHostCertificateReloadServesNewCert: the certificate manager's swap
// reaches the live listener without restarting it.
func TestHTTPHostCertificateReloadServesNewCert(t *testing.T) {
	m := selfSignedTLS(t)
	host := startHost(t, hostOptions{TLSHost: "127.0.0.1", TLSConfig: m.TLSConfig()}, "ok")
	before := m.Info().Fingerprint
	if err := m.Regenerate(); err != nil {
		t.Fatal(err)
	}
	if m.Info().Fingerprint == before {
		t.Fatal("Regenerate kept the same certificate")
	}
	resp := fetch(t, tlsClient(t, m), "https://"+host.HTTPSAddr()+"/ping")
	if resp.tls == nil || len(resp.tls.PeerCertificates) == 0 {
		t.Fatal("no peer certificate on the HTTPS response")
	}
	if got := tlscert.Fingerprint(resp.tls.PeerCertificates[0]); got != m.Info().Fingerprint {
		t.Errorf("served %s, want the regenerated %s", got, m.Info().Fingerprint)
	}
}

// TestHTTPHostBindFailure: a taken loopback port is a startup error, not a
// background log line.
func TestHTTPHostBindFailure(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer utils.CloseQuietly(ln)
	tcpAddr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("listener address %q is not TCP", ln.Addr())
	}
	host, err := newHTTPHost(hostOptions{Port: tcpAddr.Port})
	if err != nil {
		t.Fatal(err)
	}
	if err := host.Start(); err == nil {
		t.Fatal("Start succeeded on a port already in use")
	}
}

// TestHTTPHostApplyPolicy_CertificateNames: the certificate's names are
// accepted Hosts once set, and dropped when replaced.
func TestHTTPHostApplyPolicy_CertificateNames(t *testing.T) {
	h := newPolicyHost(t, config.GatewayConfig{Host: "0.0.0.0", Port: 18790})
	status := func(hostHeader string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "127.0.0.1:5000"
		req.Host = hostHeader
		h.handler.ServeHTTP(rec, req)
		return rec.Code
	}
	if got := status("box.lan:18443"); got != http.StatusMisdirectedRequest {
		t.Fatalf("unknown name before SetCertificateNames: %d", got)
	}
	h.SetCertificateNames([]string{"box.lan", "192.168.1.20"})
	if err := h.ApplyPolicy(config.GatewayConfig{Host: "0.0.0.0", Port: 18790}, nil); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"box.lan:18443", "192.168.1.20:18443"} {
		if got := status(name); got != http.StatusOK {
			t.Errorf("certificate name %q: status %d", name, got)
		}
	}
	h.SetCertificateNames([]string{"other.lan"})
	if err := h.ApplyPolicy(config.GatewayConfig{Host: "0.0.0.0", Port: 18790}, nil); err != nil {
		t.Fatal(err)
	}
	if got := status("box.lan:18443"); got != http.StatusMisdirectedRequest {
		t.Errorf("stale certificate name still served: %d", got)
	}
}

// TestNewHTTPHost_EnforcesAllowlist verifies the listener wraps its mux in the
// IP allowlist: loopback and in-range peers reach the handler; out-of-range
// peers get 403 — regardless of bind address.
func TestNewHTTPHost_EnforcesAllowlist(t *testing.T) {
	host, err := newHTTPHost(hostOptions{AllowedCIDRs: []string{"192.168.0.0/16"}})
	if err != nil {
		t.Fatalf("newHTTPHost: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/x", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	host.SetMux(mux)

	cases := []struct {
		remoteAddr string
		want       int
	}{
		{"127.0.0.1:5000", http.StatusOK},      // loopback always allowed
		{"192.168.1.10:5000", http.StatusOK},   // in private range
		{"8.8.8.8:5000", http.StatusForbidden}, // public, blocked
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.RemoteAddr = c.remoteAddr
		req.Host = "127.0.0.1"
		rec := httptest.NewRecorder()
		host.handler.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s: status = %d, want %d", c.remoteAddr, rec.Code, c.want)
		}
	}
}

func TestNewHTTPHost_InvalidCIDR(t *testing.T) {
	if _, err := newHTTPHost(hostOptions{AllowedCIDRs: []string{"not-a-cidr"}}); err == nil {
		t.Fatal("expected error for invalid CIDR")
	}
}

func waitForServer(t *testing.T, addr string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			if closeErr := conn.Close(); closeErr != nil {
				t.Errorf("close: %v", closeErr)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("server at %s did not accept within %v", addr, timeout)
}

func httpGetBody(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			t.Errorf("close body: %v", closeErr)
		}
	}()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(body)
}
