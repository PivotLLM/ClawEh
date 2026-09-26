// ClawEh
// License: MIT

package status

import (
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/internal/admin"
)

func accessConfig(t *testing.T, host, mode string) *config.Config {
	t.Helper()
	t.Setenv("CLAW_HOME", t.TempDir())
	cfg := config.DefaultConfig()
	cfg.Gateway.Host = host
	cfg.Gateway.Port = 18790
	cfg.Gateway.TLS.Mode = mode
	return cfg
}

func wantLines(t *testing.T, got string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("output missing %q:\n%s", w, got)
		}
	}
}

// Everything on localhost: the operator is told the WebUI is not served on
// the network and how to change that, and that there is no admin account yet.
func TestPrintAccess_LocalhostOnlyNoAdmin(t *testing.T) {
	got := accessReport(accessConfig(t, "127.0.0.1", config.TLSModeLocalhost))
	wantLines(t, got,
		"WebUI (localhost):  http://127.0.0.1:18790/",
		"WebUI (localhost):  https://127.0.0.1:18443/",
		"WebUI (network):    not served",
		"HTTPS is\n                      on localhost only",
		"Admin account:      none — run: claw admin",
	)
	if strings.Contains(got, "this host") {
		t.Errorf("the old label must be gone:\n%s", got)
	}
}

// HTTPS off and HTTP on loopback: nothing on the network, no certificate.
func TestPrintAccess_HTTPSOff(t *testing.T) {
	got := accessReport(accessConfig(t, "", config.TLSModeOff))
	wantLines(t, got, "WebUI (localhost):  http://127.0.0.1:18790/", `HTTPS:              off (gateway.tls.mode "off")`, "WebUI (network):    not served")
	if strings.Contains(got, "https://") || strings.Contains(got, "Certificate:") {
		t.Errorf("HTTPS off must not advertise HTTPS or a certificate:\n%s", got)
	}
}

// The default install: HTTP on localhost, HTTPS on the network with a
// certificate line explaining the browser warning (or saying the pair is not
// generated yet), and a configured admin account named.
func TestPrintAccess_DefaultWithAdmin(t *testing.T) {
	cfg := accessConfig(t, "127.0.0.1", "")
	if err := admin.Write(admin.Path(cfg.DataDir()), "alice", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	got := accessReport(cfg)
	wantLines(t, got,
		"WebUI (localhost):  http://127.0.0.1:18790/",
		"WebUI (network):    https://",
		"Certificate:        not generated yet",
		"Admin account:      alice ✓",
	)
	if strings.Contains(got, "not served") || strings.Contains(got, "https://0.0.0.0") {
		t.Errorf("HTTPS on all interfaces is served on real addresses:\n%s", got)
	}
}

// Plain HTTP on the network is listed as such, flagged unencrypted.
func TestPrintAccess_HTTPOnNetwork(t *testing.T) {
	got := accessReport(accessConfig(t, "10.0.0.5", config.TLSModeOff))
	wantLines(t, got,
		"WebUI (localhost):  http://127.0.0.1:18790/",
		"WebUI (network):    http://10.0.0.5:18790/  (plain HTTP, unencrypted)",
	)
	if strings.Contains(got, "not served") {
		t.Errorf("network HTTP is served:\n%s", got)
	}
}
