// ClawEh
// License: MIT

package device

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"testing"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/channels"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/internal/tlscert"
)

// reloadTestConfig is a configuration with the device channel enabled on a
// free loopback port, with its data directory (and the self-signed HTTPS
// certificate) under a temporary directory.
func reloadTestConfig(t *testing.T) *config.Config {
	t.Helper()
	t.Setenv(global.EnvVarHome, t.TempDir())
	cfg := config.DefaultConfig()
	cfg.Channels.Device = config.DeviceChannelConfig{
		Enabled:   true,
		Token:     "tok",
		Host:      "127.0.0.1",
		Port:      freePort(t),
		AllowFrom: config.FlexibleStringSlice{"*"},
	}
	return cfg
}

func inputsOf(t *testing.T, cfg *config.Config) string {
	t.Helper()
	b, err := json.Marshal(factoryInputs(cfg))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// writeSelfSigned writes a self-signed pair naming extra under dir/tls and
// returns its paths.
func writeSelfSigned(t *testing.T, dir, extra string) (certPath, keyPath string) {
	t.Helper()
	opts := tlscert.Options{DataDir: dir, ExtraNames: []string{extra}}
	if _, err := tlscert.Load(opts); err != nil {
		t.Fatal(err)
	}
	return opts.Paths()
}

// Each value the device factory reads changes its inputs, so a reload that
// changes it rebuilds the channel; a setting it does not read does not.
func TestFactoryInputs_CoverWhatTheFactoryReads(t *testing.T) {
	base := reloadTestConfig(t)
	certDir := t.TempDir()
	certPath, keyPath := writeSelfSigned(t, certDir, "alice.example")
	base.Gateway.TLS.CertFile, base.Gateway.TLS.KeyFile = certPath, keyPath
	before := inputsOf(t, base)

	for _, tc := range []struct {
		name   string
		change func(*config.Config)
	}{
		{"channels.device", func(c *config.Config) { c.Channels.Device.Port++ }},
		{"logging.log_message_content", func(c *config.Config) { c.Logging.LogMessageContent = !c.Logging.LogMessageContent }},
		{"gateway.external_url", func(c *config.Config) { c.Gateway.ExternalURL = "https://claw.example" }},
		{"gateway.lockout_exempt", func(c *config.Config) { c.Gateway.LockoutExempt = []string{"192.0.2.1"} }},
		{"gateway.trusted_proxies", func(c *config.Config) { c.Gateway.TrustedProxies = []string{"192.0.2.2"} }},
		{"gateway.tls.mode", func(c *config.Config) { c.Gateway.TLS.Mode = config.TLSModeOff }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := *base
			cfg.Gateway.LockoutExempt = slices.Clone(base.Gateway.LockoutExempt)
			cfg.Gateway.TrustedProxies = slices.Clone(base.Gateway.TrustedProxies)
			tc.change(&cfg)
			if inputsOf(t, &cfg) == before {
				t.Fatalf("changing %s leaves the device inputs unchanged", tc.name)
			}
		})
	}

	t.Run("certificate names", func(t *testing.T) {
		otherCert, otherKey := writeSelfSigned(t, t.TempDir(), "bob.example")
		for src, dst := range map[string]string{otherCert: certPath, otherKey: keyPath} {
			b, err := os.ReadFile(src)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(dst, b, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if inputsOf(t, base) == before {
			t.Fatal("a certificate with other names leaves the device inputs unchanged")
		}
	})

	t.Run("unrelated settings", func(t *testing.T) {
		cfg := *base
		cfg.Channels.Slack.Enabled = true
		cfg.Voice.EchoTranscription = !cfg.Voice.EchoTranscription
		cfg.Gateway.AllowedCIDRs = []string{"192.0.2.0/24"}
		if inputsOf(t, &cfg) != inputsOf(t, base) {
			t.Fatal("a setting the device channel does not read changed its inputs")
		}
	})
}

// Through the manager: a reload that leaves the device settings alone keeps
// the running device channel (its connections stay open); one that changes a
// gateway setting it reads (trusted proxies) rebuilds it on the same port.
func TestReload_DeviceChannel(t *testing.T) {
	cfg := reloadTestConfig(t)
	mb := bus.NewMessageBus()
	defer mb.Close()
	built := 0
	m, err := channels.NewManager(cfg, mb, nil, channels.WithBuildHook(func(name string, _ channels.Channel) {
		if name == "device" {
			built++
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.StartAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := m.StopAll(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	first, _ := m.GetChannel("device")

	same := *cfg
	same.Channels.Slack.AppToken = "unrelated"
	if res := m.Reload(context.Background(), &same, nil); !slices.Equal(res.Kept, []string{"device"}) {
		t.Fatalf("unrelated change: %+v, want device kept", res)
	}
	if ch, _ := m.GetChannel("device"); ch != first || !first.IsRunning() {
		t.Fatal("device channel replaced or stopped by an unrelated change")
	}

	changed := same
	changed.Gateway.TrustedProxies = []string{"192.0.2.2"}
	if res := m.Reload(context.Background(), &changed, nil); !slices.Equal(res.Restarted, []string{"device"}) {
		t.Fatalf("trusted proxies change: %+v, want device restarted", res)
	}
	second, _ := m.GetChannel("device")
	if second == first || first.IsRunning() || !second.IsRunning() {
		t.Fatal("device channel not rebuilt")
	}
	if built != 2 {
		t.Fatalf("build hook ran %d times, want 2", built)
	}
}
