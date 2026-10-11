// ClawEh
// License: MIT

package api

import (
	"slices"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
)

// The pair response's warnings are the short hover text on the Devices page:
// plain sentences with no bind address or config key in them.
func TestBuildPairResponse_Warnings(t *testing.T) {
	for _, tc := range []struct {
		name        string
		host        string
		externalURL string
		want        string
	}{
		{"localhost only", "127.0.0.1", "https://claw.example.com:8443", "Devices on your network can't connect."},
		{"invalid external address", "0.0.0.0", "ftp://", "The external address is not valid."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.Channels.Device.Host = tc.host
			cfg.Channels.Device.ExternalURL = tc.externalURL
			out := (&Handler{}).buildPairResponse(cfg, false)
			warnings, ok := out["warnings"].([]string)
			if !ok {
				t.Fatalf("warnings is %T, want []string", out["warnings"])
			}
			if !slices.Contains(warnings, tc.want) {
				t.Fatalf("warnings = %q, want %q among them", warnings, tc.want)
			}
		})
	}
}
