package device

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
)

func TestGenerateSharedToken(t *testing.T) {
	a, err := GenerateSharedToken()
	if err != nil {
		t.Fatal(err)
	}
	b, err := GenerateSharedToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 64 || a == b {
		t.Fatalf("token not 64-hex/unique: len=%d eq=%v", len(a), a == b)
	}
}

func TestEnsureProvisioned(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.SaveConfig(path, config.DefaultConfig()); err != nil {
		t.Fatal(err)
	}

	cfg, changed, err := EnsureProvisioned(path)
	if err != nil {
		t.Fatalf("EnsureProvisioned: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true on first provision")
	}
	if cfg.Channels.Device.Token == "" || !cfg.Channels.Device.Enabled {
		t.Fatalf("not provisioned: %+v", cfg.Channels.Device)
	}

	// Idempotent: a second call makes no change and keeps the same token.
	cfg2, changed2, err := EnsureProvisioned(path)
	if err != nil {
		t.Fatal(err)
	}
	if changed2 {
		t.Fatal("expected changed=false on second provision")
	}
	if cfg2.Channels.Device.Token != cfg.Channels.Device.Token {
		t.Fatal("token changed across calls")
	}

	// Persisted to disk.
	reloaded, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Channels.Device.Token != cfg.Channels.Device.Token || !reloaded.Channels.Device.Enabled {
		t.Fatalf("provision not persisted: %+v", reloaded.Channels.Device)
	}
}

// The QR payload advertises the scheme the listener actually speaks: ws by
// default, wss with channels.device.tls; an external URL decides on its own.
func TestBuildSetupPayloadScheme(t *testing.T) {
	plain, err := BuildSetupPayload("", "", []string{"10.0.0.5"}, 18791, "tok", false)
	if err != nil || plain.Protocol != "ws" {
		t.Fatalf("plain: protocol = %q, err = %v, want ws", plain.Protocol, err)
	}
	secure, err := BuildSetupPayload("", "", []string{"10.0.0.5"}, 18791, "tok", true)
	if err != nil || secure.Protocol != "wss" || secure.Port != 18791 {
		t.Fatalf("tls: protocol = %q port = %d, err = %v, want wss on 18791", secure.Protocol, secure.Port, err)
	}
	ext, err := BuildSetupPayload("https://claw.example.com:42330", "", nil, 18791, "tok", false)
	if err != nil || ext.Protocol != "wss" {
		t.Fatalf("external https: protocol = %q, err = %v, want wss", ext.Protocol, err)
	}
}

// Without an external URL the QR lists gateway.external_url's hostname first,
// then the LAN IPs, so a device tries the operator's name before the addresses.
func TestBuildSetupPayloadGatewayHostnameFirst(t *testing.T) {
	p, err := BuildSetupPayload("", "https://claw.example.com:18443", []string{"10.0.0.5", "192.168.1.9"}, 18791, "tok", false)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(p.IPs, ","), "claw.example.com,10.0.0.5,192.168.1.9"; got != want {
		t.Fatalf("ips = %q, want %q", got, want)
	}
	if p.Port != 18791 || p.Protocol != "ws" {
		t.Fatalf("port %d protocol %q, want the device listener on ws", p.Port, p.Protocol)
	}
	ext, err := BuildSetupPayload("https://ops.example.com:42330", "https://claw.example.com", []string{"10.0.0.5"}, 18791, "tok", false)
	if err != nil || strings.Join(ext.IPs, ",") != "ops.example.com" {
		t.Fatalf("external url set: ips = %v, err = %v, want only the external host", ext.IPs, err)
	}
}

// ConnectURL is the single address the Devices page shows; it follows the QR when
// an external URL is set and otherwise falls back to a host that is never blank.
func TestConnectURL(t *testing.T) {
	lan := []string{"10.0.0.5", "192.168.1.9"}
	for _, tc := range []struct {
		name       string
		dev        config.DeviceChannelConfig
		gatewayURL string
		lanIPs     []string
		want       string
	}{
		{"external https", config.DeviceChannelConfig{ExternalURL: "https://ops42.example.com:42330"}, "", lan, "wss://ops42.example.com:42330"},
		{"external wss with port", config.DeviceChannelConfig{ExternalURL: "wss://ops42.example.com:42330"}, "", lan, "wss://ops42.example.com:42330"},
		{"external https without port", config.DeviceChannelConfig{ExternalURL: "https://ops42.example.com"}, "", lan, "wss://ops42.example.com:443"},
		{"external http without port", config.DeviceChannelConfig{ExternalURL: "http://ops42.example.com"}, "", lan, "ws://ops42.example.com:80"},
		{"external ws with port", config.DeviceChannelConfig{ExternalURL: "ws://ops42.example.com:42330", TLS: true}, "", lan, "ws://ops42.example.com:42330"},
		{"external invalid falls back", config.DeviceChannelConfig{ExternalURL: "not a url", Port: 42330}, "", lan, "ws://10.0.0.5:42330"},
		{"empty with gateway hostname", config.DeviceChannelConfig{Port: 42330}, "https://claw.example.com:18443", lan, "ws://claw.example.com:42330"},
		{"empty with gateway hostname and tls", config.DeviceChannelConfig{Port: 42330, TLS: true}, "https://claw.example.com", lan, "wss://claw.example.com:42330"},
		{"empty with LAN IP only", config.DeviceChannelConfig{}, "", lan, "ws://10.0.0.5:18791"},
		{"empty with LAN IP only and tls", config.DeviceChannelConfig{TLS: true}, "", lan, "wss://10.0.0.5:18791"},
		{"empty with nothing", config.DeviceChannelConfig{}, "", nil, "ws://127.0.0.1:18791"},
		{"empty with nothing and tls", config.DeviceChannelConfig{TLS: true, Port: 42330}, "", []string{}, "wss://127.0.0.1:42330"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ConnectURL(tc.dev, tc.gatewayURL, tc.lanIPs); got != tc.want {
				t.Fatalf("ConnectURL = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRenderQRCode(t *testing.T) {
	content := `{"type":"clawdbot-gateway","version":1,"ips":["192.168.1.10"],"port":18790,"token":"abc","protocol":"ws"}`
	png, err := RenderQRCodePNGDataURL(content)
	if err != nil || !strings.HasPrefix(png, "data:image/png;base64,") {
		t.Fatalf("png render: err=%v prefix-ok=%v", err, strings.HasPrefix(png, "data:image/png;base64,"))
	}
	ascii, err := RenderQRCodeASCII(content)
	if err != nil || strings.TrimSpace(ascii) == "" {
		t.Fatalf("ascii render: err=%v empty=%v", err, strings.TrimSpace(ascii) == "")
	}
}

func TestIsLoopbackHost(t *testing.T) {
	for _, h := range []string{"", "127.0.0.1", "localhost", "::1"} {
		if !IsLoopbackHost(h) {
			t.Fatalf("expected %q loopback", h)
		}
	}
	for _, h := range []string{"0.0.0.0", "192.168.1.5", "10.0.0.1"} {
		if IsLoopbackHost(h) {
			t.Fatalf("expected %q non-loopback", h)
		}
	}
}
