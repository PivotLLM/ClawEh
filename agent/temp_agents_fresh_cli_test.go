// ClawEh
// License: MIT

package agent

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/providers"
)

// fakeClaudeCLI writes a stand-in for the claude binary that records its
// working directory, arguments and environment under logDir and answers with
// a successful result.
func fakeClaudeCLI(t *testing.T, logDir string) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "claude")
	body := "#!/bin/sh\n" +
		"pwd > '" + logDir + "/pwd'\n" +
		"for a in \"$@\"; do printf '%s\\n' \"$a\"; done > '" + logDir + "/args'\n" +
		"env > '" + logDir + "/env'\n" +
		"cat > /dev/null\n" +
		"printf '%s' '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"cli reply\"}'\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return script
}

func readLog(t *testing.T, logDir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(logDir, name))
	if err != nil {
		t.Fatalf("the CLI left no %s: %v", name, err)
	}
	return string(b)
}

func realPath(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// TestFreshAgent_CLIModelIsolated: a fresh agent on a CLI model runs the CLI
// in its own empty workspace and without the provider's bypass flags, even
// with bypass_restrictions on; a config agent on the same model runs it in
// the shared cli/ directory with the bypass flag. Neither gets the service's
// own environment.
func TestFreshAgent_CLIModelIsolated(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()
	t.Setenv("CLAW_TEST_SERVICE_SECRET", "do-not-leak")

	logDir := t.TempDir()
	cfg := ownerTestConfig(t)
	cfg.Providers = []config.Provider{{
		Name: "Claude CLI", Protocol: "claude-cli", Command: fakeClaudeCLI(t, logDir), BypassRestrictions: true,
	}}
	cfg.Models = []config.ModelConfig{{ModelName: "cc", Model: "claude-cli", Provider: "Claude CLI", Enabled: true}}
	cfg.Agents.Defaults.Models = []string{"cc"}
	if err := os.MkdirAll(cfg.CLIPath(), 0o700); err != nil {
		t.Fatal(err)
	}
	bypass := config.CLIAgentByProtocol("claude-cli").BypassArgs
	if len(bypass) == 0 {
		t.Fatal("fixture: claude-cli has no bypass flags")
	}

	al := mustNewAgentLoop(t, cfg, bus.NewMessageBus(), &recordingProvider{}, providers.NewProviderDispatcher(cfg), OwnsDataDir())
	id, err := al.GetRegistry().CreateFresh(config.AgentConfig{Models: []string{"cc"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	fresh, _ := al.GetRegistry().Get(id)

	for _, tc := range []struct {
		agent      string
		wantDir    string
		wantBypass bool
	}{
		{id, fresh.Workspace, false},
		{"main", cfg.CLIPath(), true},
	} {
		turn(t, al, tc.agent, "hello")
		if got, want := strings.TrimSpace(readLog(t, logDir, "pwd")), realPath(t, tc.wantDir); realPath(t, got) != want {
			t.Fatalf("%s: CLI ran in %s, want %s", tc.agent, got, want)
		}
		args := strings.Split(strings.TrimSpace(readLog(t, logDir, "args")), "\n")
		for _, flag := range bypass {
			if slices.Contains(args, flag) != tc.wantBypass {
				t.Fatalf("%s: args %v, bypass flag %s present = %v, want %v", tc.agent, args, flag, !tc.wantBypass, tc.wantBypass)
			}
		}
		if env := readLog(t, logDir, "env"); strings.Contains(env, "CLAW_TEST_SERVICE_SECRET") {
			t.Fatalf("%s: the service environment reached the CLI", tc.agent)
		}
	}
	assertEmptyWorkspace(t, fresh)
}
