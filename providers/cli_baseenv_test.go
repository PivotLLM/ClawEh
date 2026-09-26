// ClawEh
// License: MIT

package providers

import (
	"testing"

	"github.com/PivotLLM/spawnllm"

	"github.com/PivotLLM/ClawEh/config"
)

// TestNewCLIProvider_EveryProtocolTakesBaseEnv checks that each CLI protocol's
// provider is one newCLIProvider can hand the allowlisted environment to: were
// a CLI provider to stop implementing spawnllm.BaseEnvSetter, the type
// assertion in newCLIProvider would silently fall through and that CLI would
// again inherit the host's whole environment.
func TestNewCLIProvider_EveryProtocolTakesBaseEnv(t *testing.T) {
	for _, protocol := range []string{"claude-cli", "codex-cli", "antigravity-cli", "gemini-cli", "cursor-cli"} {
		for _, bypass := range []bool{false, true} {
			model := &config.ModelConfig{ModelName: "m", Model: "x", Provider: "P"}
			p, _, err := CreateProviderFromConfig(model, &config.Provider{Name: "P", Protocol: protocol, BypassRestrictions: bypass})
			if err != nil {
				t.Fatalf("%s bypass=%v: %v", protocol, bypass, err)
			}
			if _, ok := unwrapCLI(p).(spawnllm.BaseEnvSetter); !ok {
				t.Errorf("%s bypass=%v: %T does not implement spawnllm.BaseEnvSetter", protocol, bypass, unwrapCLI(p))
			}
		}
	}
}
