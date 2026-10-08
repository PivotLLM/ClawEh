// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import "testing"

// LaunchTool names the launch tool as the "forum" suite publishes it.
func TestLaunchToolMatchesPublishedName(t *testing.T) {
	for _, def := range Tools(nil, nil) {
		if "forum_"+def.Name == LaunchTool {
			return
		}
	}
	t.Fatalf("no tool in Tools publishes as %q", LaunchTool)
}
