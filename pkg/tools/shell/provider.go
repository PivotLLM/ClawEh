package shell

import (
	"log"

	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/tools"
)

// Provider is the singleton ToolProvider for shell tools.
var Provider shellProvider

type shellProvider struct{}

func (p shellProvider) Namespace() string   { return "shell" }
func (p shellProvider) Description() string { return "Shell command execution" }
func (p shellProvider) Category() string    { return "shell" }
func (p shellProvider) ConfigKey() string   { return "exec" }

func (p shellProvider) Available(cfg *config.Config) (bool, string) {
	return true, ""
}

func (p shellProvider) Build(deps tools.ToolDeps) []tools.Tool {
	cfg := deps.Cfg
	if cfg == nil {
		return nil
	}
	agentCfg := deps.AgentCfg

	if !cfg.Tools.IsToolEnabled("shell_exec") {
		return nil
	}
	if !isToolAllowed(agentCfg, "shell_exec") {
		return nil
	}

	execTool, err := NewExecToolWithConfig(deps.Workspace, cfg.Agents.Defaults.RestrictToWorkspace, cfg)
	if err != nil {
		log.Fatalf("Critical error: unable to initialize exec tool: %v", err)
	}
	return []tools.Tool{execTool}
}

// isToolAllowed checks whether the agent config permits the named tool.
func isToolAllowed(agentCfg *config.AgentConfig, name string) bool {
	if agentCfg == nil {
		return true
	}
	return agentCfg.IsToolAllowed(name)
}
