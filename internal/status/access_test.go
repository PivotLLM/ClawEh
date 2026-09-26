// ClawEh
// License: MIT

package status

import (
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/internal/admin"
)

func accessConfig(t *testing.T, host string) *config.Config {
	t.Helper()
	t.Setenv("CLAW_HOME", t.TempDir())
	cfg := config.DefaultConfig()
	cfg.Gateway.Host = host
	cfg.Gateway.Port = 18790
	return cfg
}

// A loopback bind serves the WebUI on this host only; the operator must be
// told that, and told there is no admin account yet, before they try a browser.
func TestPrintAccess_LoopbackNoAdmin(t *testing.T) {
	cfg := accessConfig(t, "127.0.0.1")
	got := accessReport(cfg)

	for _, want := range []string{
		"http://127.0.0.1:18790/",
		"WebUI (network):    not served",
		"Admin account:      none — run: claw admin",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "https://") {
		t.Errorf("loopback bind must not advertise an HTTPS URL:\n%s", got)
	}
}

// A network bind is the headless case: the URL is https on tls_port, the
// certificate line explains the browser warning (or says the pair is not
// generated yet), and a configured admin account is named.
func TestPrintAccess_NetworkBindWithAdmin(t *testing.T) {
	cfg := accessConfig(t, "10.0.0.5")
	if err := admin.Write(admin.Path(cfg.DataDir()), "alice", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	got := accessReport(cfg)

	for _, want := range []string{
		"http://127.0.0.1:18790/",
		"https://10.0.0.5:18443/",
		"Certificate:        not generated yet",
		"Admin account:      alice ✓",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
}

// A wildcard bind is expanded to real interface addresses, never printed as
// https://0.0.0.0, which no browser can open.
func TestNetworkHosts_ExpandsWildcard(t *testing.T) {
	for _, h := range networkHosts("0.0.0.0") {
		if h == "0.0.0.0" && len(networkHosts("0.0.0.0")) > 1 {
			t.Errorf("wildcard leaked into the host list: %v", networkHosts("0.0.0.0"))
		}
	}
	if got := networkHosts("10.0.0.5"); len(got) != 1 || got[0] != "10.0.0.5" {
		t.Errorf("specific bind = %v, want [10.0.0.5]", got)
	}
}
