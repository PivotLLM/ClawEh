package config

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/logger"
)

// The permission-bypass flag is passed only when the provider's
// bypass_restrictions is on; the non-security flags a CLI needs to run headless
// are passed either way, so a model that carries no extra_args at all — one
// written before the catalogue, or added through the WebUI — still runs.
func TestCLIArgs_BypassFlagsFollowTheProviderSetting(t *testing.T) {
	tests := []struct {
		name     string
		protocol string
		bypass   bool
		extra    []string
		want     []string
	}{
		{name: "claude off", protocol: "claude-cli", want: []string{"--no-chrome"}},
		{name: "claude on", protocol: "claude-cli", bypass: true, want: []string{"--dangerously-skip-permissions", "--no-chrome"}},
		{name: "codex off", protocol: "codex-cli", want: []string{"--skip-git-repo-check"}},
		{name: "codex on", protocol: "codex-cli", bypass: true, want: []string{"--dangerously-bypass-approvals-and-sandbox", "--skip-git-repo-check"}},
		{name: "antigravity off", protocol: "antigravity-cli", want: nil},
		{name: "antigravity on", protocol: "antigravity-cli", bypass: true, want: []string{"--dangerously-skip-permissions"}},
		{name: "cursor off", protocol: "cursor-cli", want: nil},
		{name: "cursor on", protocol: "cursor-cli", bypass: true, want: []string{"--yolo"}},
		{
			name:     "model args are appended, not substituted",
			protocol: "antigravity-cli",
			bypass:   true,
			extra:    []string{"--model-arg"},
			want:     []string{"--dangerously-skip-permissions", "--model-arg"},
		},
		{
			// A model that already carries the flag must not pass it twice.
			name:     "a flag given explicitly is not duplicated",
			protocol: "cursor-cli",
			bypass:   true,
			extra:    []string{"--yolo", "--other"},
			want:     []string{"--yolo", "--other"},
		},
		{
			// The checkbox decides, not a flag left in a model from before it.
			name:     "a bypass flag in extra_args is stripped when bypass is off",
			protocol: "codex-cli",
			extra:    []string{"--dangerously-bypass-approvals-and-sandbox", "--other"},
			want:     []string{"--skip-git-repo-check", "--other"},
		},
		{
			name:     "the deprecated gemini-cli alias gets antigravity's flags",
			protocol: "gemini-cli",
			bypass:   true,
			want:     []string{"--dangerously-skip-permissions"},
		},
		{
			name:     "an HTTP protocol is left alone",
			protocol: "openai-chat",
			bypass:   true,
			extra:    []string{"--x"},
			want:     []string{"--x"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := CLIArgs(tc.protocol, tc.bypass, tc.extra)
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("CLIArgs = %v, want %v", got, tc.want)
			}
		})
	}
}

// Stripping a flag silently would leave an operator wondering why the CLI
// refuses tools when the config plainly lists the flag. One warning, naming the
// checkbox, per invocation.
func TestCLIArgs_WarnsOnceWhenStrippingABypassFlag(t *testing.T) {
	var buf bytes.Buffer
	restore := logger.RedirectForTest(&buf)
	defer restore()

	CLIArgs("claude-cli", false, []string{"--dangerously-skip-permissions", "--verbose"})
	out := buf.String()
	if !strings.Contains(out, "Bypass CLI restrictions") || !strings.Contains(out, "--dangerously-skip-permissions") {
		t.Errorf("no warning naming the checkbox and the flag:\n%s", out)
	}
	if strings.Count(out, "Bypass CLI restrictions") != 1 {
		t.Errorf("want exactly one warning:\n%s", out)
	}

	buf.Reset()
	CLIArgs("claude-cli", true, []string{"--dangerously-skip-permissions"})
	CLIArgs("claude-cli", false, []string{"--verbose"})
	if buf.Len() != 0 {
		t.Errorf("warned with nothing to strip:\n%s", buf.String())
	}
}

// The setting defaults off: a provider that says nothing about it runs the CLI
// under the CLI's own permissions.
func TestProvider_BypassRestrictionsDefaultsOff(t *testing.T) {
	cfg := DefaultConfig()
	for i := range cfg.Providers {
		if p := &cfg.Providers[i]; IsCLIProtocol(p.Protocol) && p.BypassRestrictions {
			t.Errorf("seeded provider %q has bypass_restrictions on", p.Name)
		}
	}
	if (Provider{}).BypassRestrictions {
		t.Error("zero-value provider has bypass_restrictions on")
	}
}

func TestCLIEnv(t *testing.T) {
	got := CLIEnv("claude-cli", nil)
	if got["CLAUDE_CODE_DISABLE_AUTO_MEMORY"] != "1" {
		t.Errorf("claude-cli env = %v, want the auto-memory opt-out", got)
	}
	// A model may override the catalogue, but must not lose the rest of it.
	got = CLIEnv("claude-cli", map[string]string{"CLAUDE_CODE_DISABLE_AUTO_MEMORY": "0", "OTHER": "x"})
	if got["CLAUDE_CODE_DISABLE_AUTO_MEMORY"] != "0" || got["OTHER"] != "x" {
		t.Errorf("model env did not override cleanly: %v", got)
	}
	if got := CLIEnv("openai-chat", nil); got != nil {
		t.Errorf("HTTP protocol env = %v, want nil", got)
	}
}

// The catalogue drives the WebUI's CLI section, the provider factory and the
// seeded config, so every row has to be complete and every accepted CLI
// protocol has to have a row.
func TestCLIAgents_CatalogueIsComplete(t *testing.T) {
	for _, c := range CLIAgents {
		if c.Protocol == "" || c.Label == "" || c.Binary == "" {
			t.Errorf("incomplete row: %+v", c)
		}
		if len(c.BypassArgs) == 0 {
			t.Errorf("%s has no bypass arguments; every CLI has a permission-bypass flag the checkbox controls", c.Protocol)
		}
		for _, a := range c.RequiredArgs {
			if slices.Contains(c.BypassArgs, a) {
				t.Errorf("%s lists %s as both required and bypass", c.Protocol, a)
			}
		}
		// The base arguments are what makes the CLI answer in JSON at all. A
		// row without them would show the operator a command line shorter than
		// the one being run, which is the failure this field exists to prevent.
		if len(c.BaseArgs) == 0 {
			t.Errorf("%s publishes no base arguments", c.Protocol)
		}
		if !slices.Contains(c.BaseArgs, "json") && !slices.Contains(c.BaseArgs, "--json") {
			t.Errorf("%s base args %v request no JSON output", c.Protocol, c.BaseArgs)
		}
		if c.RequestTimeout <= 0 {
			t.Errorf("%s has no request timeout", c.Protocol)
		}
		if !IsCLIProtocol(c.Protocol) {
			t.Errorf("%s is in the catalogue but not an accepted CLI protocol", c.Protocol)
		}
		if c.SentinelModel() != c.Protocol {
			t.Errorf("%s sentinel = %q", c.Protocol, c.SentinelModel())
		}
	}
	for _, protocol := range []string{"claude-cli", "codex-cli", "antigravity-cli", "cursor-cli", "gemini-cli"} {
		if CLIAgentByProtocol(protocol) == nil {
			t.Errorf("%s is an accepted CLI protocol with no catalogue row", protocol)
		}
	}
	if CLIAgentByProtocol("openai-chat") != nil {
		t.Error("an HTTP protocol resolved to a CLI agent")
	}
}

// The seeded models carry no flags of their own: the catalogue supplies them,
// so a fresh install and a toggled-on CLI run with the same command line, and
// no seeded model smuggles in a permission-bypass flag the checkbox is off for.
func TestSeededCLIModelsMatchTheCatalogue(t *testing.T) {
	cfg := DefaultConfig()
	byName := map[string]*Provider{}
	for i := range cfg.Providers {
		byName[cfg.Providers[i].Name] = &cfg.Providers[i]
	}
	seen := map[string]bool{}
	for i := range cfg.Models {
		m := &cfg.Models[i]
		prov := byName[m.Provider]
		if prov == nil || !IsCLIProtocol(prov.Protocol) {
			continue
		}
		agent := CLIAgentByProtocol(prov.Protocol)
		if agent == nil {
			t.Errorf("seeded model %q uses CLI protocol %q with no catalogue row", m.ModelName, prov.Protocol)
			continue
		}
		if m.Model == agent.SentinelModel() {
			seen[prov.Protocol] = true
		}
		if len(m.ExtraArgs) != 0 {
			t.Errorf("seeded model %q carries extra_args %v; the catalogue supplies a CLI's flags",
				m.ModelName, m.ExtraArgs)
		}
		if got := CLIArgs(prov.Protocol, prov.BypassRestrictions, m.ExtraArgs); !slices.Equal(got, agent.RequiredArgs) {
			t.Errorf("seeded model %q runs with %v, want the catalogue's required args %v and no bypass flag",
				m.ModelName, got, agent.RequiredArgs)
		}
		if m.RequestTimeout != agent.RequestTimeout {
			t.Errorf("seeded model %q timeout = %d, catalogue says %d",
				m.ModelName, m.RequestTimeout, agent.RequestTimeout)
		}
	}
	for _, c := range CLIAgents {
		if !seen[c.Protocol] {
			t.Errorf("no seeded sentinel model for %s", c.Protocol)
		}
	}
}
