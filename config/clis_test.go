package config

import (
	"slices"
	"testing"
)

// A CLI model written before this table existed — or added through the WebUI,
// which has no field for arguments — carries no extra_args at all. It must
// still run with its permission flag, because without it the CLI auto-denies
// the tool call and answers nothing.
func TestCLIArgs_SuppliesTheRequiredFlags(t *testing.T) {
	tests := []struct {
		name     string
		protocol string
		extra    []string
		want     []string
	}{
		{
			name:     "no extra args at all",
			protocol: "antigravity-cli",
			want:     []string{"--dangerously-skip-permissions"},
		},
		{
			name:     "model args are appended, not substituted",
			protocol: "antigravity-cli",
			extra:    []string{"--model-arg"},
			want:     []string{"--dangerously-skip-permissions", "--model-arg"},
		},
		{
			// A model that already carries the flag must not pass it twice.
			name:     "a flag given explicitly is not duplicated",
			protocol: "cursor-cli",
			extra:    []string{"--yolo", "--other"},
			want:     []string{"--yolo", "--other"},
		},
		{
			name:     "the deprecated gemini-cli alias gets antigravity's flags",
			protocol: "gemini-cli",
			want:     []string{"--dangerously-skip-permissions"},
		},
		{
			name:     "an HTTP protocol is left alone",
			protocol: "openai-chat",
			extra:    []string{"--x"},
			want:     []string{"--x"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := CLIArgs(tc.protocol, tc.extra); !slices.Equal(got, tc.want) {
				t.Errorf("CLIArgs = %v, want %v", got, tc.want)
			}
		})
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
		if len(c.RequiredArgs) == 0 {
			t.Errorf("%s has no required arguments; every CLI needs its permission flag", c.Protocol)
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

// The seeded models were the only working examples of CLI configuration, and
// they are now the catalogue's second copy. Keep them in step, or a fresh
// install and a toggled-on CLI would run with different flags.
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
		if !slices.Equal(CLIArgs(prov.Protocol, m.ExtraArgs), m.ExtraArgs) {
			t.Errorf("seeded model %q args %v disagree with the catalogue %v",
				m.ModelName, m.ExtraArgs, agent.RequiredArgs)
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
