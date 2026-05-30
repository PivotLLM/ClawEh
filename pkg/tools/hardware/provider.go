package hardware

import (
	"runtime"

	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/tools"
)

// Provider is the singleton ToolProvider for hardware tools.
var Provider hardwareProvider

type hardwareProvider struct{}

func (p hardwareProvider) Namespace() string   { return "hardware" }
func (p hardwareProvider) Description() string { return "Hardware I2C/SPI bus interaction" }
func (p hardwareProvider) Category() string    { return "hardware" }
func (p hardwareProvider) ConfigKey() string   { return "hardware" }

func (p hardwareProvider) Available(cfg *config.Config) (bool, string) {
	if runtime.GOOS != "linux" {
		return false, "requires Linux"
	}
	return true, ""
}

func (p hardwareProvider) Build(deps tools.ToolDeps) []tools.Tool {
	cfg := deps.Cfg
	if cfg == nil {
		return nil
	}
	agentCfg := deps.AgentCfg

	var result []tools.Tool

	if cfg.Tools.IsToolEnabled("hw_i2c") && isToolAllowed(agentCfg, "hw_i2c") {
		result = append(result, NewI2CTool())
	}
	if cfg.Tools.IsToolEnabled("hw_spi") && isToolAllowed(agentCfg, "hw_spi") {
		result = append(result, NewSPITool())
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
