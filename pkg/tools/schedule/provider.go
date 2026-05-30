package schedule

import (
	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/tools"
)

// Provider is the singleton ToolProvider for schedule tools.
// Note: the cron tool is registered dynamically via agentLoop.RegisterTool() in
// the gateway after the MCP server starts. The provider's Build() is called by the
// normal provider loop but the gateway also calls RegisterTool for the cron tool
// to handle re-registration on config reload.
var Provider scheduleProvider

type scheduleProvider struct{}

func (p scheduleProvider) Namespace() string   { return "schedule" }
func (p scheduleProvider) Description() string { return "Cron scheduling and job management" }
func (p scheduleProvider) Category() string    { return "schedule" }
func (p scheduleProvider) ConfigKey() string   { return "cron" }

func (p scheduleProvider) Available(cfg *config.Config) (bool, string) {
	return true, ""
}

func (p scheduleProvider) Describe() []tools.ToolDescriptor {
	return []tools.ToolDescriptor{
		{Name: "schedule_cron", Description: "Schedule one-time or recurring reminders, jobs, and shell commands.", Category: "automation", ConfigKey: "schedule_cron", DefaultEnabled: true},
	}
}

func (p scheduleProvider) Build(deps tools.ToolDeps) []tools.Tool {
	// The cron tool is created externally (in gateway/helpers.go) due to its
	// dependency on CronService and AgentLoop, then registered via
	// agentLoop.RegisterTool(). This provider intentionally returns nil —
	// the gateway wiring handles cron tool construction and registration.
	return nil
}
