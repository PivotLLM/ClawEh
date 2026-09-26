package config

import (
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestEffectiveExternalURL(t *testing.T) {
	hostName := func(t *testing.T, got string) string {
		t.Helper()
		u, err := url.Parse(got)
		if err != nil {
			t.Fatalf("got %q is not a valid URL: %v", got, err)
		}
		return u.Hostname()
	}

	t.Run("mode all (the default) is https on the tls port with the host name", func(t *testing.T) {
		for _, host := range []string{"", "127.0.0.1", "0.0.0.0", "192.168.1.50"} {
			g := GatewayConfig{Host: host, Port: 8080, TLSPort: 9443}
			got := g.EffectiveExternalURL()
			if !strings.HasPrefix(got, "https://") || !strings.HasSuffix(got, ":9443") {
				t.Fatalf("host %q: got %q, want an https://<name>:9443 url", host, got)
			}
			h := hostName(t, got)
			if h == "0.0.0.0" {
				t.Errorf("host %q: got %q, must not advertise the wildcard", host, got)
			}
			if hn, err := os.Hostname(); err == nil && hn != "" && !IsLoopbackHost(hn) && h != hn {
				t.Errorf("host %q: advertised %q, want the machine host name %q", host, h, hn)
			}
		}
		if got := (GatewayConfig{}).EffectiveExternalURL(); !strings.HasSuffix(got, ":18443") {
			t.Errorf("got %q, want the :18443 default", got)
		}
	})

	t.Run("mode localhost is https on 127.0.0.1", func(t *testing.T) {
		g := GatewayConfig{Host: "0.0.0.0", Port: 8080, TLSPort: 9443, TLS: TLSConfig{Mode: TLSModeLocalhost}}
		if got, want := g.EffectiveExternalURL(), "https://127.0.0.1:9443"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("mode off follows the plain-HTTP listener", func(t *testing.T) {
		off := TLSConfig{Mode: TLSModeOff}
		for _, host := range []string{"", "127.0.0.1", "localhost", "::1"} {
			g := GatewayConfig{Host: host, Port: 8080, TLS: off}
			if got, want := g.EffectiveExternalURL(), "http://127.0.0.1:8080"; got != want {
				t.Errorf("host %q: got %q, want %q", host, got, want)
			}
		}
		g := GatewayConfig{Host: "192.168.1.50", Port: 9000, TLS: off}
		if got, want := g.EffectiveExternalURL(), "http://192.168.1.50:9000"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		g = GatewayConfig{Host: "0.0.0.0", Port: 9000, TLS: off}
		got := g.EffectiveExternalURL()
		if !strings.HasPrefix(got, "http://") || !strings.HasSuffix(got, ":9000") || strings.Contains(got, "0.0.0.0") {
			t.Errorf("wildcard, https off: got %q, want http://<name>:9000", got)
		}
	})

	t.Run("explicit override returned verbatim", func(t *testing.T) {
		g := GatewayConfig{Host: "0.0.0.0", Port: 8080, ExternalURL: "http://claw.local:1234"}
		if got, want := g.EffectiveExternalURL(), "http://claw.local:1234"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		g = GatewayConfig{Host: "127.0.0.1", Port: 8080, ExternalURL: "https://claw.example.com", TLS: TLSConfig{Mode: TLSModeOff}}
		if got, want := g.EffectiveExternalURL(), "https://claw.example.com"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

func TestTLSMode(t *testing.T) {
	for _, c := range []struct {
		mode, want string
		https      bool
	}{
		{"", TLSModeAll, true},
		{"all", TLSModeAll, true},
		{" ALL ", TLSModeAll, true},
		{"localhost", TLSModeLocalhost, true},
		{"off", TLSModeOff, false},
	} {
		g := GatewayConfig{TLS: TLSConfig{Mode: c.mode}}
		if got := g.TLS.EffectiveMode(); got != c.want {
			t.Errorf("mode %q: effective %q, want %q", c.mode, got, c.want)
		}
		if g.HTTPSEnabled() != c.https {
			t.Errorf("mode %q: HTTPSEnabled = %v, want %v", c.mode, g.HTTPSEnabled(), c.https)
		}
		if err := g.Validate(); err != nil {
			t.Errorf("mode %q: %v", c.mode, err)
		}
	}
	err := (GatewayConfig{TLS: TLSConfig{Mode: "lan"}}).Validate()
	if err == nil || !strings.Contains(err.Error(), "gateway.tls.mode") {
		t.Errorf("unknown mode: err = %v, want a gateway.tls.mode error", err)
	}
}

func TestGatewayBindHosts(t *testing.T) {
	for _, c := range []struct {
		host string
		want []string
	}{
		{"", []string{"127.0.0.1", "::1"}},
		{"localhost", []string{"127.0.0.1", "::1"}},
		{"[::1]", []string{"127.0.0.1", "::1"}},
		{"0.0.0.0", []string{"0.0.0.0"}},
		{"::", []string{"0.0.0.0"}},
		{"192.168.1.5", []string{"127.0.0.1", "::1", "192.168.1.5"}},
	} {
		if got := (GatewayConfig{Host: c.host}).HTTPBindHosts(); !slices.Equal(got, c.want) {
			t.Errorf("HTTP host %q: %v, want %v", c.host, got, c.want)
		}
	}
	for _, c := range []struct {
		mode string
		want []string
	}{
		{"", []string{"0.0.0.0"}},
		{TLSModeLocalhost, []string{"127.0.0.1", "::1"}},
		{TLSModeOff, nil},
	} {
		// HTTPS placement does not follow gateway.host.
		for _, host := range []string{"127.0.0.1", "0.0.0.0", "10.0.0.5"} {
			g := GatewayConfig{Host: host, TLS: TLSConfig{Mode: c.mode}}
			if got := g.HTTPSBindHosts(); !slices.Equal(got, c.want) {
				t.Errorf("mode %q host %q: %v, want %v", c.mode, host, got, c.want)
			}
		}
	}
	if (GatewayConfig{Host: "127.0.0.1", TLS: TLSConfig{Mode: TLSModeLocalhost}}).ReachableOffBox() {
		t.Error("loopback HTTP and localhost HTTPS must not be reachable off-box")
	}
	if !(GatewayConfig{Host: "127.0.0.1"}).ReachableOffBox() {
		t.Error("HTTPS on all interfaces is reachable off-box")
	}
	if !(GatewayConfig{Host: "0.0.0.0", TLS: TLSConfig{Mode: TLSModeOff}}).ReachableOffBox() {
		t.Error("network HTTP is reachable off-box")
	}
}

func TestGatewayURLs(t *testing.T) {
	g := GatewayConfig{Port: 18790, TLSPort: 18443, TLS: TLSConfig{Mode: TLSModeLocalhost}}
	if got := g.LocalHTTPURL(); got != "http://127.0.0.1:18790/" {
		t.Errorf("LocalHTTPURL = %q", got)
	}
	if got := g.NetworkHTTPURLs(); got != nil {
		t.Errorf("loopback NetworkHTTPURLs = %v, want nil", got)
	}
	if got := g.HTTPSURLs(); !slices.Equal(got, []string{"https://127.0.0.1:18443/"}) {
		t.Errorf("localhost HTTPSURLs = %v", got)
	}
	g.Host = "10.0.0.5"
	if got := g.NetworkHTTPURLs(); !slices.Equal(got, []string{"http://10.0.0.5:18790/"}) {
		t.Errorf("network HTTP URLs = %v", got)
	}
	g.TLS.Mode = TLSModeOff
	if got := g.HTTPSURLs(); got != nil {
		t.Errorf("off HTTPSURLs = %v, want nil", got)
	}
	g.TLS.Mode = TLSModeAll
	for _, u := range g.HTTPSURLs() {
		if !strings.HasPrefix(u, "https://") || !strings.HasSuffix(u, ":18443/") || strings.Contains(u, "127.0.0.1") {
			t.Errorf("all-interfaces HTTPS URL %q", u)
		}
	}
}

func TestGatewayListeners(t *testing.T) {
	a := GatewayConfig{}.Listeners()
	b := GatewayConfig{Host: "localhost", Port: DefaultGatewayPort, TLSPort: DefaultGatewayTLSPort, TLS: TLSConfig{Mode: "all"}}.Listeners()
	if a != b {
		t.Errorf("defaults and explicit defaults differ: %+v vs %+v", a, b)
	}
	if c := (GatewayConfig{Port: 9000}).Listeners(); c == a {
		t.Error("a different port must differ")
	}
	// With HTTPS off, its port and certificate bind nothing.
	off1 := GatewayConfig{TLSPort: 1, TLS: TLSConfig{Mode: TLSModeOff, CertFile: "/a", KeyFile: "/b"}}.Listeners()
	off2 := GatewayConfig{TLSPort: 2, TLS: TLSConfig{Mode: TLSModeOff}}.Listeners()
	if off1 != off2 {
		t.Errorf("HTTPS off: %+v vs %+v", off1, off2)
	}
	if f := (GatewayConfig{TLS: TLSConfig{CertFile: "/a", KeyFile: "/b"}}).Listeners(); f.CertFile != "/a" || f == a {
		t.Errorf("certificate files must count with HTTPS on: %+v", f)
	}
}

func TestGatewayNetworkAccess(t *testing.T) {
	if !(GatewayConfig{Host: "0.0.0.0"}).NetworkAccess() {
		t.Error("0.0.0.0 should report network access")
	}
	if (GatewayConfig{Host: "127.0.0.1"}).NetworkAccess() {
		t.Error("127.0.0.1 should not report network access")
	}
}

func TestTLSConfigValidate(t *testing.T) {
	cases := []struct {
		name    string
		tls     TLSConfig
		wantErr bool
	}{
		{"neither", TLSConfig{}, false},
		{"both", TLSConfig{CertFile: "a.crt", KeyFile: "a.key"}, false},
		{"cert only", TLSConfig{CertFile: "a.crt"}, true},
		{"key only", TLSConfig{KeyFile: "a.key"}, true},
	}
	for _, c := range cases {
		err := c.tls.Validate()
		if (err != nil) != c.wantErr {
			t.Errorf("%s: err = %v, wantErr %v", c.name, err, c.wantErr)
		}
		if err != nil && (!strings.Contains(err.Error(), "cert_file") || !strings.Contains(err.Error(), "key_file")) {
			t.Errorf("%s: error must name both keys: %v", c.name, err)
		}
	}
	if (TLSConfig{CertFile: "a", KeyFile: "b"}).UserSupplied() != true || (TLSConfig{}).UserSupplied() {
		t.Error("UserSupplied must be true only with both files")
	}
}

func TestGatewayValidate_PortsMustDiffer(t *testing.T) {
	if err := (GatewayConfig{Host: "127.0.0.1", Port: 18443}).Validate(); err == nil {
		t.Error("tls_port equal to port must be rejected when HTTPS is on")
	}
	if err := (GatewayConfig{Host: "127.0.0.1", Port: 18443, TLS: TLSConfig{Mode: TLSModeOff}}).Validate(); err != nil {
		t.Errorf("with HTTPS off there is no HTTPS listener; got %v", err)
	}
	for _, g := range []GatewayConfig{{Port: -1}, {Port: 70000}, {TLSPort: 70000}} {
		if err := g.Validate(); err == nil || !strings.Contains(err.Error(), "out of valid range") {
			t.Errorf("%+v: err = %v, want out of range", g, err)
		}
	}
}

func TestValidateMCPHostListen(t *testing.T) {
	for _, ok := range []string{"", "127.0.0.1:5911", "[::1]:5911", "localhost:5911"} {
		if err := ValidateMCPHostListen(ok); err != nil {
			t.Errorf("%q: unexpected error %v", ok, err)
		}
	}
	for _, bad := range []string{"0.0.0.0:5911", "192.168.1.5:5911", "[::]:5911", "5911", "claw.lan:5911"} {
		if err := ValidateMCPHostListen(bad); err == nil {
			t.Errorf("%q: must be rejected", bad)
		}
	}
}

// TestLoadConfig_ListenerValidation checks that LoadConfig itself refuses a
// half-configured certificate and an off-box MCP host, so the config watcher
// reports them and the gateway never starts on them.
func TestLoadConfig_ListenerValidation(t *testing.T) {
	write := func(t *testing.T, body string) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if _, err := LoadConfig(write(t, `{"gateway":{"host":"0.0.0.0","tls":{"cert_file":"/x.crt"}}}`)); err == nil {
		t.Error("cert_file without key_file must fail to load")
	} else if !strings.Contains(err.Error(), "key_file") {
		t.Errorf("error must name the missing key: %v", err)
	}
	if _, err := LoadConfig(write(t, `{"mcp_host":{"enabled":true,"listen":"0.0.0.0:5911"}}`)); err == nil {
		t.Error("off-box mcp_host.listen must fail to load")
	}
	if _, err := LoadConfig(write(t, `{"gateway":{"host":"0.0.0.0","tls":{"cert_file":"/x.crt","key_file":"/x.key"}},"mcp_host":{"listen":"127.0.0.1:5911"}}`)); err != nil {
		t.Errorf("valid listeners must load: %v", err)
	}
}

// A wildcard bind is expanded to real interface addresses, never returned as
// 0.0.0.0 (which no browser can open) unless the machine has no address.
func TestNetworkHosts_ExpandsWildcard(t *testing.T) {
	hosts := NetworkHosts("0.0.0.0")
	if len(hosts) > 1 && slices.Contains(hosts, "0.0.0.0") {
		t.Errorf("wildcard leaked into the host list: %v", hosts)
	}
	if got := NetworkHosts("10.0.0.5"); len(got) != 1 || got[0] != "10.0.0.5" {
		t.Errorf("specific bind = %v, want [10.0.0.5]", got)
	}
}

// Docker's interfaces are recognised by the names Docker gives them; real
// NICs and other bridges are not.
func TestIsContainerInterface(t *testing.T) {
	for name, want := range map[string]bool{
		"docker0":          true,
		"docker_gwbridge":  true,
		"br-a1672a2308d0":  true,
		"veth3f2a1b0":      true,
		"eth0":             false,
		"enp4s0":           false,
		"wlp3s0":           false,
		"br0":              false, // a hand-made bridge, not a Docker network
		"bridge0":          false,
		"lo":               false,
		"tailscale0":       false,
		"virbr0":           false,
		"docker-not-a-nic": true,
	} {
		if got := isContainerInterface(name); got != want {
			t.Errorf("isContainerInterface(%q) = %v, want %v", name, got, want)
		}
	}
}

// The advertised list is the network list minus container bridge addresses,
// never empty, and untouched for a specific bind.
func TestAdvertisedHosts_DropsContainerBridges(t *testing.T) {
	all := NetworkHosts("0.0.0.0")
	adv := AdvertisedHosts("0.0.0.0")
	if len(adv) == 0 {
		t.Fatal("advertised list is empty")
	}
	for _, h := range adv {
		if !slices.Contains(all, h) {
			t.Errorf("advertised %q is not a network host (%v)", h, all)
		}
	}
	if hidden := containerAddrs(); len(adv) < len(all) || len(hidden) == 0 {
		// Only when something was dropped can a bridge address be absent; on
		// a machine without Docker the two lists are the same.
		for _, h := range adv {
			if hidden[h] && len(adv) != len(all) {
				t.Errorf("container address %q advertised: %v", h, adv)
			}
		}
	}
	if got := AdvertisedHosts("172.17.0.1"); len(got) != 1 || got[0] != "172.17.0.1" {
		t.Errorf("specific bind = %v, want [172.17.0.1]", got)
	}
}
