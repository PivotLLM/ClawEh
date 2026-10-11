package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTrustedProxySet_ClientAddr(t *testing.T) {
	set, err := CompileTrustedProxies([]string{"127.0.0.1", " 10.0.0.0/8 "})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, remote, realIP, xff, want string
	}{
		{"trusted peer, X-Real-IP", "127.0.0.1:5000", "203.0.113.7", "", "203.0.113.7"},
		{"X-Real-IP wins over X-Forwarded-For", "10.1.2.3:5000", "203.0.113.7", "198.51.100.1", "203.0.113.7"},
		{"fallback to first X-Forwarded-For entry", "10.1.2.3:5000", "", " 198.51.100.1 , 10.1.2.3", "198.51.100.1"},
		{"unparseable X-Real-IP falls back", "127.0.0.1:5000", "bogus", "198.51.100.1", "198.51.100.1"},
		{"trusted peer, no usable header", "127.0.0.1:5000", "", "junk", "127.0.0.1"},
		{"IPv4-mapped header is unmapped", "127.0.0.1:5000", "::ffff:203.0.113.9", "", "203.0.113.9"},
		{"IPv6 header", "127.0.0.1:5000", "2001:db8::5", "", "2001:db8::5"},
		{"untrusted peer: headers ignored", "198.51.100.9:5000", "203.0.113.7", "203.0.113.8", "198.51.100.9"},
		{"loopback is not implied", "[::1]:5000", "203.0.113.7", "", "::1"},
		{"peer without a port", "10.9.9.9", "203.0.113.7", "", "203.0.113.7"},
	} {
		if got := set.ClientAddr(tc.remote, tc.realIP, tc.xff); got != tc.want {
			t.Errorf("%s: ClientAddr = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestTrustedProxySet_NilTrustsNoOne(t *testing.T) {
	var set *TrustedProxySet
	if got := set.ClientAddr("127.0.0.1:5000", "203.0.113.7", "203.0.113.8"); got != "127.0.0.1" {
		t.Fatalf("nil set: ClientAddr = %q, want the peer", got)
	}
	empty, err := CompileTrustedProxies(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := empty.ClientAddr("127.0.0.1:5000", "203.0.113.7", ""); got != "127.0.0.1" {
		t.Fatalf("empty set: ClientAddr = %q, want the peer", got)
	}
}

func TestCompileTrustedProxies_RejectsBadEntries(t *testing.T) {
	for _, bad := range []string{"nope", "10.0.0.0/99", "fe80::1%eth0", "", "*"} {
		_, err := CompileTrustedProxies([]string{bad})
		if err == nil {
			t.Errorf("entry %q accepted", bad)
			continue
		}
		if !strings.Contains(err.Error(), "gateway.trusted_proxies") {
			t.Errorf("entry %q: error %q does not name the key", bad, err)
		}
	}
}

func TestGatewayValidate_TrustedProxies(t *testing.T) {
	if err := (GatewayConfig{TrustedProxies: []string{"192.0.2.1", "10.0.0.0/8"}}).Validate(); err != nil {
		t.Fatalf("valid list rejected: %v", err)
	}
	if err := (GatewayConfig{TrustedProxies: []string{"nope"}}).Validate(); err == nil {
		t.Fatal("invalid entry accepted")
	}
}

func TestLoadConfig_RejectsBadTrustedProxies(t *testing.T) {
	t.Setenv("CLAW_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"gateway":{"trusted_proxies":["10.0.0.0/99"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "gateway.trusted_proxies") {
		t.Fatalf("LoadConfig error = %v, want a gateway.trusted_proxies error", err)
	}
}

func TestDeviceValidateExposure(t *testing.T) {
	for _, tc := range []struct {
		name    string
		dev     DeviceChannelConfig
		wantKey string // "" means allowed
	}{
		{"network, auto_approve", DeviceChannelConfig{Enabled: true, Host: "0.0.0.0", Token: "t", AutoApprove: true}, "channels.device.auto_approve"},
		{"network, no secrets", DeviceChannelConfig{Enabled: true, Host: "192.168.1.5"}, "channels.device.token"},
		{"network, token set", DeviceChannelConfig{Enabled: true, Host: "0.0.0.0", Token: "t"}, ""},
		{"network, word token only", DeviceChannelConfig{Enabled: true, Host: "0.0.0.0", WordToken: "a-b-c-d-e"}, ""},
		{"loopback, auto_approve and no secrets", DeviceChannelConfig{Enabled: true, Host: "127.0.0.1", AutoApprove: true}, ""},
		{"default host is loopback", DeviceChannelConfig{Enabled: true, AutoApprove: true}, ""},
		{"disabled", DeviceChannelConfig{Host: "0.0.0.0", AutoApprove: true}, ""},
	} {
		err := tc.dev.ValidateExposure()
		switch {
		case tc.wantKey == "" && err != nil:
			t.Errorf("%s: refused: %v", tc.name, err)
		case tc.wantKey != "" && (err == nil || !strings.Contains(err.Error(), tc.wantKey)):
			t.Errorf("%s: err = %v, want one naming %s", tc.name, err, tc.wantKey)
		}
	}
}

func TestLoadConfig_RefusesExposedOpenDeviceGateway(t *testing.T) {
	t.Setenv("CLAW_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "config.json")
	body := `{"channels":{"device":{"enabled":true,"host":"0.0.0.0","token":"t","auto_approve":true}}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "channels.device.auto_approve") {
		t.Fatalf("LoadConfig error = %v, want the auto_approve refusal", err)
	}
}

func TestProxyNotTrusted(t *testing.T) {
	for _, tc := range []struct {
		name        string
		external    string
		devExternal string
		devEnabled  bool
		trusted     []string
		seenPeer    string
		want        bool
	}{
		{name: "nothing set"},
		{name: "external URL, no trusted proxy", external: "https://claw.example.com", want: true},
		{name: "external URL, loopback trusted", external: "https://claw.example.com", trusted: []string{"127.0.0.1"}},
		{name: "external URL, IPv6 loopback trusted", external: "https://claw.example.com", trusted: []string{"::1"}},
		{name: "external URL, other proxy trusted", external: "https://claw.example.com", trusted: []string{"10.0.0.5"}, want: true},
		{name: "device external URL", devEnabled: true, devExternal: "https://claw.example.com", want: true},
		{name: "device external URL, device off", devExternal: "https://claw.example.com"},
		{name: "forwarded request seen", seenPeer: "127.0.0.1", want: true},
		{name: "forwarded request seen, peer trusted", seenPeer: "127.0.0.1", trusted: []string{"127.0.0.0/8"}},
		{name: "forwarded request seen from the other loopback", seenPeer: "::1", trusted: []string{"127.0.0.1"}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{}
			cfg.Gateway.ExternalURL = tc.external
			cfg.Gateway.TrustedProxies = tc.trusted
			cfg.Channels.Device.Enabled = tc.devEnabled
			cfg.Channels.Device.ExternalURL = tc.devExternal
			if got := cfg.ProxyNotTrusted(tc.seenPeer); got != tc.want {
				t.Errorf("ProxyNotTrusted(%q) = %v, want %v", tc.seenPeer, got, tc.want)
			}
		})
	}
}

func TestBehindProxy(t *testing.T) {
	for _, tc := range []struct {
		name        string
		trusted     []string
		external    string
		tlsMode     string
		devEnabled  bool
		devTLS      bool
		devExternal string
		want        []string
	}{
		{name: "nothing set"},
		{name: "trusted proxy", trusted: []string{"10.0.0.5", " 10.0.1.0/24 "}, want: []string{"10.0.0.5", "10.0.1.0/24"}},
		{name: "https external URL on another port", external: "https://claw.example.com", want: []string{"https://claw.example.com"}},
		{name: "https external URL served by ClawEh", external: "https://claw.lan:18443"},
		{name: "https external URL with HTTPS off", external: "https://claw.lan:18443", tlsMode: TLSModeOff, want: []string{"https://claw.lan:18443"}},
		{name: "http external URL", external: "http://claw.lan:18790"},
		{name: "wss device URL, device plain", devEnabled: true, devExternal: "wss://dev.example.com", want: []string{"wss://dev.example.com"}},
		{name: "wss device URL served by the device listener", devEnabled: true, devTLS: true, devExternal: "wss://claw.lan:18791"},
		{name: "device URL, device off", devExternal: "wss://dev.example.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{}
			cfg.Gateway.TrustedProxies = tc.trusted
			cfg.Gateway.ExternalURL = tc.external
			cfg.Gateway.TLS.Mode = tc.tlsMode
			cfg.Channels.Device.Enabled = tc.devEnabled
			cfg.Channels.Device.TLS = tc.devTLS
			cfg.Channels.Device.ExternalURL = tc.devExternal
			got := cfg.BehindProxy()
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Errorf("BehindProxy() = %q, want %q", got, tc.want)
			}
		})
	}
}
