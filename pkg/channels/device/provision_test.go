package device

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/config"
)

func TestGenerateSharedToken(t *testing.T) {
	a, err := GenerateSharedToken()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := GenerateSharedToken()
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
