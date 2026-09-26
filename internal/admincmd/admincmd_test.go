// ClawEh
// License: MIT

package admincmd

import (
	"os"
	"strings"
	"testing"
)

func TestResolveHome_EnvWins(t *testing.T) {
	t.Setenv("CLAW_HOME", "/tmp/claw-test-home")
	home, _ := ResolveHome()
	if home != "/tmp/claw-test-home" {
		t.Fatalf("ResolveHome() = %q", home)
	}
}

func TestValidateUsername(t *testing.T) {
	for _, tc := range []struct {
		in string
		ok bool
	}{
		{"alice", true},
		{"", false},
		{"al ice", false},
		{strings.Repeat("a", 65), false},
	} {
		if err := validateUsername(tc.in); (err == nil) != tc.ok {
			t.Errorf("validateUsername(%q) err = %v, want ok=%v", tc.in, err, tc.ok)
		}
	}
}

func TestRun_RequiresTerminal(t *testing.T) {
	// Under `go test` stdin is not a terminal, which is the case the check
	// exists for: a password must never be read from a pipe or a redirect.
	err := run(os.Stdout, "alice")
	if err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Fatalf("run() without a terminal = %v", err)
	}
}
