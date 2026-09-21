// ClawEh
// License: MIT

package audit

import "testing"

func TestCollectIdentity(t *testing.T) {
	cfg, env := fixtureConfig(t)
	s := collectIdentity(t.Context(), cfg, env)
	tb := s.Tables[0]
	_, r := findRow(t, tb, "Runs as")
	if r[1] != "user eric, group staff" {
		t.Errorf("Runs as = %q", r[1])
	}
	_, r = findRow(t, tb, "Version")
	if r[1] != "0.6.0+abcdef12" {
		t.Errorf("Version = %q", r[1])
	}
	_, r = findRow(t, tb, "Data directory")
	if r[1] != env.DataDir {
		t.Errorf("Data directory = %q, want %q", r[1], env.DataDir)
	}
}
