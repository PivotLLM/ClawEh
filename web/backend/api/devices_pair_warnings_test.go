// ClawEh
// License: MIT

package api

import (
	"slices"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
)

// The pair response's warnings are the short hover text on the Devices page:
// plain sentences with no bind address or config key in them. The whole list
// is compared, so a stray extra warning fails too.
func TestBuildPairResponse_Warnings(t *testing.T) {
	lan := []string{"192.168.1.20"}
	for _, tc := range []struct {
		name        string
		host        string
		externalURL string
		lanIPs      []string
		want        []string
	}{
		{"localhost only", "127.0.0.1", "https://claw.example.com:8443", lan, []string{"Devices on your network can't connect."}},
		{"invalid external address", "0.0.0.0", "ftp://", lan, []string{"The external address is not valid."}},
		{"localhost and invalid external address", "127.0.0.1", "ftp://", lan, []string{
			"Devices on your network can't connect.",
			"The external address is not valid.",
		}},
		{"working setup", "0.0.0.0", "wss://claw.example.com", lan, []string{}},
		{"working setup on the LAN", "0.0.0.0", "", lan, []string{}},
		{"no network address", "0.0.0.0", "", nil, []string{"No network address found; set an external address."}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.Channels.Device.Host = tc.host
			cfg.Channels.Device.ExternalURL = tc.externalURL
			lanIPs := tc.lanIPs
			out := (&Handler{lanIPv4s: func() []string { return lanIPs }}).buildPairResponse(cfg, false)
			warnings, ok := out["warnings"].([]string)
			if !ok {
				t.Fatalf("warnings is %T, want []string", out["warnings"])
			}
			if !slices.Equal(warnings, tc.want) {
				t.Fatalf("warnings = %q, want %q", warnings, tc.want)
			}
		})
	}
}
