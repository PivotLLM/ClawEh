// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"strings"
	"testing"
)

// TestGuide_HostName pins that the guide names the host as the host passes it
// in, falls back to a neutral word, and never shows the placeholder.
func TestGuide_HostName(t *testing.T) {
	tests := []struct {
		name, productName, want string
	}{
		{"named", "Alice", "set up and Alice runs for you"},
		{"unnamed", "", "set up and the host runs for you"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := guide(Ceilings{}, tt.productName)
			if !strings.Contains(got, tt.want) {
				t.Errorf("guide does not contain %q", tt.want)
			}
			if strings.Contains(got, hostPlaceholder) {
				t.Errorf("guide still holds %s", hostPlaceholder)
			}
		})
	}
}
