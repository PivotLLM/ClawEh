// ClawEh
// License: MIT

package report

import (
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
)

func TestCollectNetwork_Listeners(t *testing.T) {
	cfg, env := fixtureConfig(t)
	tb := findTable(t, collectNetwork(t.Context(), cfg, env), "Listeners")

	_, gw := findRow(t, tb, "Gateway")
	if gw[1] != "127.0.0.1:18790 (HTTP), 0.0.0.0:18443 (HTTPS)" {
		t.Errorf("gateway bind = %q", gw[1])
	}
	contains(t, gw[2], "192.168.1.0/24", "gateway allowlist")
	contains(t, gw[3], "reachable from other hosts", "gateway off-host note")

	_, dev := findRow(t, tb, "Device gateway")
	if dev[1] != "0.0.0.0:18791" {
		t.Errorf("device bind = %q (port must default to 18791)", dev[1])
	}
	contains(t, dev[2], "any address", "device allowlist")

	_, line := findRow(t, tb, "LINE webhook")
	if line[1] != "0.0.0.0:18792/webhook/line" {
		t.Errorf("line bind = %q", line[1])
	}

	// Proxy credentials never appear.
	s := collectNetwork(t.Context(), cfg, env)
	pt := findTable(t, s, "Origins and proxies")
	txt := tableText(pt)
	contains(t, txt, "proxy.local:3128 (credentials redacted)", "proxy row")
	if strings.Contains(txt, secretProxyPass) {
		t.Error("proxy password leaked into the network table")
	}
}

func TestGatewayAllow(t *testing.T) {
	cases := []struct {
		cidrs []string
		want  string
	}{
		{nil, "loopback only (allowed_cidrs is empty)"},
		{[]string{config.AllowAnyAddress}, "any address (allowed_cidrs contains *)"},
		{[]string{"10.0.0.0/8"}, "10.0.0.0/8 (loopback always allowed)"},
	}
	for _, c := range cases {
		if got := gatewayAllow(c.cidrs); got != c.want {
			t.Errorf("gatewayAllow(%v) = %q, want %q", c.cidrs, got, c.want)
		}
	}
}

func TestCollectNetwork_LoopbackHasNoOffHostNote(t *testing.T) {
	cfg, env := fixtureConfig(t)
	cfg.Gateway.Host = "127.0.0.1"
	cfg.Gateway.AllowedCIDRs = nil
	cfg.MCPHost.Enabled = false
	cfg.MCPHost.AutoEnable = false
	tb := findTable(t, collectNetwork(t.Context(), cfg, env), "Listeners")
	_, gw := findRow(t, tb, "Gateway")
	if strings.Contains(gw[3], "reachable from other hosts") {
		t.Errorf("loopback gateway must not carry the off-host note: %q", gw[3])
	}
	contains(t, gw[2], "loopback only", "empty allowlist")
	_, mcp := findRow(t, tb, "MCP host")
	if mcp[1] != disabled {
		t.Errorf("MCP host should be disabled, got %q", mcp[1])
	}
}
