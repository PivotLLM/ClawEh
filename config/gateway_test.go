package config

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEffectiveExternalURL(t *testing.T) {
	t.Run("loopback gateway is plain http on 127.0.0.1", func(t *testing.T) {
		for _, host := range []string{"", "127.0.0.1", "localhost", "::1"} {
			g := GatewayConfig{Host: host, Port: 8080}
			if got, want := g.EffectiveExternalURL(), "http://127.0.0.1:8080"; got != want {
				t.Errorf("host %q: got %q, want %q", host, got, want)
			}
		}
	})

	t.Run("loopback default port", func(t *testing.T) {
		g := GatewayConfig{}
		if got, want := g.EffectiveExternalURL(), "http://127.0.0.1:18790"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("bind-all resolves to https on the tls port with a real host", func(t *testing.T) {
		g := GatewayConfig{Host: "0.0.0.0", Port: 8080, TLSPort: 9443}
		got := g.EffectiveExternalURL()
		if !strings.HasPrefix(got, "https://") || !strings.HasSuffix(got, ":9443") {
			t.Fatalf("got %q, want an https://<host>:9443 url", got)
		}
		// Whatever the environment, the host must not be the wildcard address.
		if strings.Contains(got, "0.0.0.0") {
			t.Errorf("got %q, must not advertise 0.0.0.0", got)
		}
		u, err := url.Parse(got)
		if err != nil {
			t.Fatalf("got %q is not a valid URL: %v", got, err)
		}
		if hn, err := os.Hostname(); err == nil && hn != "" && !IsLoopbackHost(hn) && u.Hostname() != hn {
			t.Errorf("host = %q, want the machine host name %q", u.Hostname(), hn)
		}
	})

	t.Run("bind-all uses the default tls port", func(t *testing.T) {
		g := GatewayConfig{Host: "0.0.0.0", Port: 18790}
		if got := g.EffectiveExternalURL(); !strings.HasSuffix(got, ":18443") {
			t.Errorf("got %q, want the :18443 default", got)
		}
	})

	t.Run("specific host used verbatim over https", func(t *testing.T) {
		g := GatewayConfig{Host: "192.168.1.50", Port: 9000, TLSPort: 9443}
		if got, want := g.EffectiveExternalURL(), "https://192.168.1.50:9443"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		g = GatewayConfig{Host: "claw.lan", Port: 9000}
		if got, want := g.EffectiveExternalURL(), "https://claw.lan:18443"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("explicit override returned verbatim", func(t *testing.T) {
		g := GatewayConfig{Host: "0.0.0.0", Port: 8080, ExternalURL: "http://claw.local:1234"}
		if got, want := g.EffectiveExternalURL(), "http://claw.local:1234"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("https override returned verbatim", func(t *testing.T) {
		g := GatewayConfig{Host: "127.0.0.1", Port: 8080, ExternalURL: "https://claw.example.com"}
		if got, want := g.EffectiveExternalURL(), "https://claw.example.com"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

func TestGatewayNetworkAccess(t *testing.T) {
	if !(GatewayConfig{Host: "0.0.0.0"}).NetworkAccess() {
		t.Error("0.0.0.0 should report network access")
	}
	if (GatewayConfig{Host: "127.0.0.1"}).NetworkAccess() {
		t.Error("127.0.0.1 should not report network access")
	}
}

func TestGatewayHTTPSEnabled(t *testing.T) {
	for _, host := range []string{"", "127.0.0.1", "localhost", "::1", "[::1]", " localhost "} {
		if (GatewayConfig{Host: host}).HTTPSEnabled() {
			t.Errorf("host %q: HTTPS must be off on loopback", host)
		}
	}
	for _, host := range []string{"0.0.0.0", "::", "192.168.1.5", "claw.lan"} {
		if !(GatewayConfig{Host: host}).HTTPSEnabled() {
			t.Errorf("host %q: HTTPS must be on off-loopback", host)
		}
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
	if err := (GatewayConfig{Host: "0.0.0.0", Port: 18443}).Validate(); err == nil {
		t.Error("tls_port equal to port must be rejected when HTTPS is on")
	}
	if err := (GatewayConfig{Host: "127.0.0.1", Port: 18443}).Validate(); err != nil {
		t.Errorf("loopback gateway has no HTTPS listener; got %v", err)
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
