package tools

import (
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/config"
)

type defaultEnabledTestProvider struct{ descs []ToolDescriptor }

func (p defaultEnabledTestProvider) Namespace() string                       { return "ztest_defaulton" }
func (p defaultEnabledTestProvider) Description() string                     { return "" }
func (p defaultEnabledTestProvider) Category() string                        { return "test" }
func (p defaultEnabledTestProvider) ConfigKey() string                       { return "ztest" }
func (p defaultEnabledTestProvider) Available(*config.Config) (bool, string) { return true, "" }
func (p defaultEnabledTestProvider) Build(ToolDeps) []Tool                   { return nil }
func (p defaultEnabledTestProvider) Describe() []ToolDescriptor              { return p.descs }

// TestDefaultEnabledToolNames verifies the single source of truth for
// "tools on by default": only DefaultEnabled descriptors are returned, across
// registered providers and the static descriptors.
func TestDefaultEnabledToolNames(t *testing.T) {
	RegisterProvider(defaultEnabledTestProvider{descs: []ToolDescriptor{
		{Name: "ztest_on", DefaultEnabled: true},
		{Name: "ztest_off", DefaultEnabled: false},
	}})

	names := DefaultEnabledToolNames()
	has := func(n string) bool {
		for _, x := range names {
			if x == n {
				return true
			}
		}
		return false
	}

	if !has("ztest_on") {
		t.Error("DefaultEnabled provider tool ztest_on missing from result")
	}
	if has("ztest_off") {
		t.Error("DefaultEnabled=false tool ztest_off must not be included")
	}
	if !has("find_tools_regex") {
		t.Error("DefaultEnabled static tool find_tools_regex missing from result")
	}
}
