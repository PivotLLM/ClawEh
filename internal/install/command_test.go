package install

import (
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/global"
)

func TestBuildUnit_RunsAsUserAndStartsAtBoot(t *testing.T) {
	t.Setenv(global.EnvVarHome, "") // ensure default data dir → no CLAW_HOME line
	// The unit must bake the user's interactive PATH (so CLI agents in ~/.local/bin
	// and an nvm node bin are reachable) with binDir first and system dirs appended.
	t.Setenv("PATH", "/home/alice/.local/bin:/home/alice/.nvm/versions/node/v24/bin:/usr/bin")
	unit := buildUnit("alice", "alice", "/home/alice/bin/claw", "/home/alice/bin")

	wants := []string{
		"User=alice",
		"Group=alice",
		"ExecStart=/home/alice/bin/claw",
		"WantedBy=multi-user.target",
		"Environment=PATH=/home/alice/bin:/home/alice/.local/bin:/home/alice/.nvm/versions/node/v24/bin:/usr/bin:/usr/local/bin:/bin",
	}
	for _, w := range wants {
		if !strings.Contains(unit, w) {
			t.Errorf("unit missing %q:\n%s", w, unit)
		}
	}
	if strings.Contains(unit, global.EnvVarHome+"=") {
		t.Errorf("unit should omit %s when default data dir is used:\n%s", global.EnvVarHome, unit)
	}
}

func TestBuildUnit_IncludesClawHomeWhenSet(t *testing.T) {
	t.Setenv(global.EnvVarHome, "/srv/claw-data")
	unit := buildUnit("bob", "staff", "/home/bob/.local/bin/claw", "/home/bob/.local/bin")

	if !strings.Contains(unit, "Environment="+global.EnvVarHome+"=/srv/claw-data") {
		t.Errorf("unit should carry CLAW_HOME when set:\n%s", unit)
	}
}

func TestServicePATH_PrependsBinDirAndDedups(t *testing.T) {
	// binDir already present in PATH must not be duplicated; system dirs appended once.
	t.Setenv("PATH", "/home/bob/bin:/usr/bin:/home/bob/.local/bin")
	got := servicePATH("/home/bob/bin")
	want := "/home/bob/bin:/usr/bin:/home/bob/.local/bin:/usr/local/bin:/bin"
	if got != want {
		t.Errorf("servicePATH = %q, want %q", got, want)
	}
}

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"":              "''",
		"/etc/claw":     "'/etc/claw'",
		"it's":          `'it'"'"'s'`,
		"/tmp/a b/claw": "'/tmp/a b/claw'",
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}
