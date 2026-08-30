package config

import (
	"net/url"
	"strings"
	"testing"
)

func TestEffectiveExternalURL(t *testing.T) {
	t.Run("loopback default", func(t *testing.T) {
		for _, host := range []string{"", "127.0.0.1", "localhost"} {
			g := GatewayConfig{Host: host, Port: 8080}
			if got, want := g.EffectiveExternalURL(), "http://127.0.0.1:8080"; got != want {
				t.Errorf("host %q: got %q, want %q", host, got, want)
			}
		}
	})

	t.Run("bind-all resolves to non-empty http url", func(t *testing.T) {
		g := GatewayConfig{Host: "0.0.0.0", Port: 8080}
		got := g.EffectiveExternalURL()
		if !strings.HasPrefix(got, "http://") || !strings.HasSuffix(got, ":8080") {
			t.Fatalf("got %q, want an http://<host>:8080 url", got)
		}
		// Whatever the environment, the host must not be the wildcard address.
		if strings.Contains(got, "0.0.0.0") {
			t.Errorf("got %q, must not advertise 0.0.0.0", got)
		}
		if _, err := url.Parse(got); err != nil {
			t.Errorf("got %q is not a valid URL: %v", got, err)
		}
	})

	t.Run("specific host used verbatim", func(t *testing.T) {
		g := GatewayConfig{Host: "192.168.1.50", Port: 9000}
		if got, want := g.EffectiveExternalURL(), "http://192.168.1.50:9000"; got != want {
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
