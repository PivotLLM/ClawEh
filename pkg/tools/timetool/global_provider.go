// ClawEh
// License: MIT

package timetool

import (
	"github.com/PivotLLM/ClawEh/pkg/global"
	"github.com/PivotLLM/ClawEh/pkg/tools"
)

// GlobalProvider exposes the time tool through the transport-neutral global
// layer with the BARE name "now". The aggregator mounts it under the "time"
// namespace, so the published name is "time_now".
var GlobalProvider globalTimeProvider

type globalTimeProvider struct{}

func (globalTimeProvider) Namespace() string   { return "time" }
func (globalTimeProvider) Description() string { return "Current date, time, and timezone" }

func (globalTimeProvider) Available(_ any) (bool, string) { return true, "" }

func (globalTimeProvider) RegisterTools(_ global.Deps) []global.ToolDefinition {
	now := NewNowTool()
	return []global.ToolDefinition{
		{
			Name:        now.Name(),
			Description: now.Description(),
			RawSchema:   now.Parameters(),
			Category:    "context",
			// On by default: the system prompt carries only a date, and the
			// prompt points the model here for the time. Withholding the tool
			// while telling the model to use it is the one combination that
			// leaves it unable to answer a question about the current time.
			DefaultAllow: global.Allow(true),
			Handler: func(call *global.ToolCall) (*global.Result, error) {
				return tools.ResultToGlobal(now.Execute(call.Ctx, call.Args)), nil
			},
		},
	}
}
