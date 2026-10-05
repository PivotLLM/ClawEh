// ClawEh
// License: MIT

package report

import (
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
)

// TestAssessment_DeviceRows: the Device HTTP/HTTPS rows read like the WebUI
// rows. Plain WebSocket on the network is unencrypted and marked; with
// channels.device.tls the HTTPS row takes over, never marked for a
// self-signed certificate; one of the two is always Disabled.
func TestAssessment_DeviceRows(t *testing.T) {
	cases := []struct {
		name          string
		enabled, tls  bool
		host          string
		cidrs         []string
		wantHTTPMark  string
		wantHTTP      string
		wantHTTPSMark string
		wantHTTPS     string
	}{
		{
			"network, plain", true, false, "0.0.0.0", nil,
			"*", "Enabled for network access (unencrypted); allowed from any address.",
			"", "Disabled.",
		},
		{
			"network, plain, allowlist", true, false, "0.0.0.0",
			[]string{"10.0.0.0/8"},
			"*", "Enabled for network access (unencrypted); allowed networks: 10.0.0.0/8.",
			"", "Disabled.",
		},
		{
			"localhost, plain", true, false, "127.0.0.1", nil,
			"", "Enabled for localhost.",
			"", "Disabled.",
		},
		{
			"network, tls", true, true, "0.0.0.0", nil,
			"", "Disabled.",
			"", "Enabled for network access, self-signed certificate; allowed from any address.",
		},
		{
			"localhost, tls", true, true, "127.0.0.1", nil,
			"", "Disabled.",
			"", "Enabled for localhost, self-signed certificate.",
		},
		{
			"disabled", false, true, "0.0.0.0", nil,
			"", "Disabled.",
			"", "Disabled.",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg, env := fixtureConfig(t)
			cfg.Channels.Device.Enabled = c.enabled
			cfg.Channels.Device.TLS = c.tls
			cfg.Channels.Device.Host = c.host
			cfg.Channels.Device.AllowedCIDRs = c.cidrs
			s := collectAssessment(t.Context(), cfg, env)
			if r := assessmentRow(t, s, "Device HTTP"); r[0] != c.wantHTTPMark || r[2] != c.wantHTTP {
				t.Errorf("HTTP row = %q, want [%q _ %q]", r, c.wantHTTPMark, c.wantHTTP)
			}
			if r := assessmentRow(t, s, "Device HTTPS"); r[0] != c.wantHTTPSMark || r[2] != c.wantHTTPS {
				t.Errorf("HTTPS row = %q, want [%q _ %q]", r, c.wantHTTPSMark, c.wantHTTPS)
			}
			for _, row := range s.Tables[0].Rows {
				if strings.Contains(strings.ToLower(row[2]), "not available") {
					t.Errorf("a row says not available: %q", row)
				}
			}
		})
	}
}

// TestAssessment_NoAuthenticationRow: login is always required, so the table
// carries no authentication row to say so.
func TestAssessment_NoAuthenticationRow(t *testing.T) {
	cfg, env := fixtureConfig(t)
	s := collectAssessment(t.Context(), cfg, env)
	for _, row := range s.Tables[0].Rows {
		if strings.Contains(strings.ToLower(row[1]), "authentication") {
			t.Errorf("unexpected authentication row: %q", row)
		}
	}
}

// TestAssessment_AnySenderSkipsAuthenticatedChannels: the WebUI's only sender
// is the logged-in operator and every device is paired, so their allow_from
// never lands them under "channels accepting any sender".
func TestAssessment_AnySenderSkipsAuthenticatedChannels(t *testing.T) {
	cfg, env := fixtureConfig(t)
	cfg.Channels.WebUI.Enabled = true
	cfg.Channels.WebUI.AllowFrom = config.FlexibleStringSlice{"*"}
	cfg.Channels.Device.Enabled = true
	cfg.Channels.Device.AllowFrom = config.FlexibleStringSlice{"*"}
	s := collectAssessment(t.Context(), cfg, env)
	r := assessmentRow(t, s, "Channels accepting any sender")
	for _, name := range []string{"webui", "device"} {
		if strings.Contains(r[2], name) {
			t.Errorf("%s listed as accepting any sender: %q", name, r)
		}
	}
}
