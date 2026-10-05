package gateway

import (
	"net"
	"net/http"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/utils"
)

// modeHostOptions builds the listener options the gateway derives from gw,
// on ephemeral ports.
func modeHostOptions(gw config.GatewayConfig, tlsCfg func() hostOptions) hostOptions {
	opts := tlsCfg()
	opts.HTTPHosts = gw.HTTPBindHosts()
	if gw.HTTPSEnabled() {
		opts.TLSHosts = gw.HTTPSBindHosts()
	}
	return opts
}

// addrHosts returns the IPs of bound addresses.
func addrHosts(t *testing.T, addrs []string) []net.IP {
	t.Helper()
	out := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		h, _, err := net.SplitHostPort(a)
		if err != nil {
			t.Fatalf("bad address %q: %v", a, err)
		}
		out = append(out, net.ParseIP(h))
	}
	return out
}

// TestHTTPHostBindsPerTLSMode: gateway.tls.mode places the HTTPS listener —
// every interface for "all", loopback only for "localhost", nowhere for
// "off" — while plain HTTP stays on loopback.
func TestHTTPHostBindsPerTLSMode(t *testing.T) {
	m := selfSignedTLS(t)
	withTLS := func() hostOptions { return hostOptions{TLSConfig: m.TLSConfig()} }
	for _, tc := range []struct {
		mode      string
		wantHTTPS string // "wildcard", "loopback" or "" (none)
	}{
		{"", "wildcard"},
		{config.TLSModeAll, "wildcard"},
		{config.TLSModeLocalhost, "loopback"},
		{config.TLSModeOff, ""},
	} {
		t.Run("mode="+tc.mode, func(t *testing.T) {
			gw := config.GatewayConfig{Host: "127.0.0.1", TLS: config.TLSConfig{Mode: tc.mode}}
			host := startHost(t, modeHostOptions(gw, withTLS), "pong")
			for _, ip := range addrHosts(t, host.HTTPAddrs()) {
				if !ip.IsLoopback() {
					t.Errorf("plain HTTP bound off loopback: %v", host.HTTPAddrs())
				}
			}
			httpsIPs := addrHosts(t, host.HTTPSAddrs())
			switch tc.wantHTTPS {
			case "":
				if len(httpsIPs) != 0 || host.tlsServer != nil {
					t.Fatalf("HTTPS bound with mode off: %v", host.HTTPSAddrs())
				}
				return
			case "wildcard":
				if len(httpsIPs) != 1 || !httpsIPs[0].IsUnspecified() {
					t.Fatalf("HTTPS addrs = %v, want one wildcard bind", host.HTTPSAddrs())
				}
			case "loopback":
				if len(httpsIPs) == 0 {
					t.Fatal("no HTTPS listener")
				}
				for _, ip := range httpsIPs {
					if !ip.IsLoopback() {
						t.Errorf("localhost mode bound %v", host.HTTPSAddrs())
					}
				}
			}
			// Either way HTTPS answers on the IPv4 loopback.
			_, port, err := net.SplitHostPort(host.HTTPSAddr())
			if err != nil {
				t.Fatal(err)
			}
			resp := fetch(t, tlsClient(t, m), "https://127.0.0.1:"+port+"/ping")
			if resp.status != http.StatusOK || resp.body != "pong" {
				t.Errorf("HTTPS on loopback: %d %q", resp.status, resp.body)
			}
		})
	}
}

// TestHTTPHostHTTPOnNetwork: a wildcard gateway.host serves plain HTTP on
// every interface with one bind, still reachable on 127.0.0.1; a specific
// address is bound in addition to the loopback pair.
func TestHTTPHostHTTPOnNetwork(t *testing.T) {
	noTLS := func() hostOptions { return hostOptions{} }
	off := config.TLSConfig{Mode: config.TLSModeOff}

	host := startHost(t, modeHostOptions(config.GatewayConfig{Host: "0.0.0.0", TLS: off}, noTLS), "open")
	ips := addrHosts(t, host.HTTPAddrs())
	if len(ips) != 1 || !ips[0].IsUnspecified() {
		t.Fatalf("HTTP addrs = %v, want one wildcard bind", host.HTTPAddrs())
	}
	if got := httpGetBody(t, "http://"+host.LoopbackAddr()+"/ping"); got != "open" {
		t.Errorf("loopback over the wildcard bind: %q", got)
	}

	// 127.0.0.2 stands in for a LAN address: it is not one of the loopback
	// names, so it is bound as a specific network address.
	probe, err := net.Listen("tcp", "127.0.0.2:0")
	if err != nil {
		t.Skip("127.0.0.2 not available on this host")
	}
	utils.CloseQuietly(probe)
	lan := config.GatewayConfig{Host: "127.0.0.2", TLS: off}
	host = startHost(t, modeHostOptions(lan, noTLS), "lan")
	// The bind address joins the accepted Host names.
	if err = host.ApplyPolicy(lan, nil); err != nil {
		t.Fatal(err)
	}
	var sawV4Loopback, sawSpecific bool
	for _, ip := range addrHosts(t, host.HTTPAddrs()) {
		switch ip.String() {
		case "127.0.0.1":
			sawV4Loopback = true
		case "127.0.0.2":
			sawSpecific = true
		}
	}
	if !sawV4Loopback || !sawSpecific {
		t.Fatalf("HTTP addrs = %v, want 127.0.0.1 and 127.0.0.2", host.HTTPAddrs())
	}
	_, port, err := net.SplitHostPort(host.LoopbackAddr())
	if err != nil {
		t.Fatal(err)
	}
	if got := httpGetBody(t, "http://127.0.0.2:"+port+"/ping"); got != "lan" {
		t.Errorf("specific address: %q", got)
	}
}
