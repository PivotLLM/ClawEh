package msg

import (
	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/tools"
)

// Provider is the singleton ToolProvider for messaging tools.
var Provider msgProvider

type msgProvider struct{}

func (p msgProvider) Namespace() string   { return "msg" }
func (p msgProvider) Description() string { return "Message sending to users and channels" }
func (p msgProvider) Category() string    { return "messaging" }
func (p msgProvider) ConfigKey() string   { return "message" }

func (p msgProvider) Available(cfg *config.Config) (bool, string) {
	return true, ""
}

func (p msgProvider) Build(deps tools.ToolDeps) []tools.Tool {
	cfg := deps.Cfg
	if cfg == nil {
		return nil
	}
	agentCfg := deps.AgentCfg

	var result []tools.Tool

	// msg_send: shared instance passed via deps.MessageTool (may be nil if not yet wired)
	if cfg.Tools.IsToolEnabled("message") && isToolAllowed(agentCfg, "msg_send") {
		if deps.MessageTool != nil {
			result = append(result, deps.MessageTool)
		}
	}

	// msg_send_file
	if cfg.Tools.IsToolEnabled("send_file") && isToolAllowed(agentCfg, "msg_send_file") {
		sendFileTool := NewSendFileTool(
			deps.Workspace,
			cfg.Agents.Defaults.RestrictToWorkspace,
			cfg.Agents.Defaults.GetMaxMediaSize(),
			nil, // MediaStore injected later by SetMediaStore
		)
		result = append(result, sendFileTool)
	}

	return result
}

// isToolAllowed checks whether the agent config permits the named tool.
func isToolAllowed(agentCfg *config.AgentConfig, name string) bool {
	if agentCfg == nil {
		return true
	}
	return agentCfg.IsToolAllowed(name)
}
