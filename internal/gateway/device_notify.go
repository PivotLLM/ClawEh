// ClawEh
// License: MIT

package gateway

import (
	"github.com/PivotLLM/ClawEh/agent"
	"github.com/PivotLLM/ClawEh/devices"
)

// defaultAgentTarget resolves where USB device notifications go: the default
// agent's default channel (its default binding, as cron output uses). It reads
// the live config and registry on every call, so a reload's new default takes
// effect without rebuilding the device service.
func defaultAgentTarget(al *agent.AgentLoop) devices.TargetFunc {
	return func() (string, string, bool) {
		cfg := al.GetConfig()
		if cfg == nil {
			return "", "", false
		}
		channel, chatID, _, ok := cfg.CronTarget(al.GetRegistry().GetDefaultAgentID())
		return channel, chatID, ok
	}
}
