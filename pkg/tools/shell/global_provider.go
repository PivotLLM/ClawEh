package shell

import (
	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/global"
	"github.com/PivotLLM/ClawEh/pkg/tools"
)

// GlobalProvider exposes the shell exec tool through the transport-neutral
// global layer with the BARE name "exec". The aggregator mounts it under the
// "shell" namespace, so the published tool name is "shell_exec". It reuses the
// existing ExecTool logic and converts the result at the boundary, so behaviour
// is unchanged.
var GlobalProvider globalShellProvider

type globalShellProvider struct{}

// Namespace/Description/Available satisfy global.HostMeta.
func (globalShellProvider) Namespace() string   { return "shell" }
func (globalShellProvider) Description() string { return "Shell command execution" }

func (globalShellProvider) Available(cfg any) (bool, string) { return true, "" }

func (globalShellProvider) RegisterTools(deps global.Deps) []global.ToolDefinition {
	// Construct the real ExecTool only when real config is present. Enumeration
	// (Describe) passes a zero Deps; handlers are never called then, so leaving
	// the instance nil is safe. On construction error we leave it nil and
	// continue (the legacy Build path log.Fatalf's; here we degrade gracefully).
	var real *ExecTool
	c, _ := deps.Cfg.(*config.Config)
	cd, _ := deps.Host.(tools.ToolDeps)
	if c != nil {
		t, err := NewExecToolWithConfig(cd.Workspace, c.Agents.Defaults.RestrictToWorkspace, c)
		if err == nil {
			real = t
		}
	}

	// Static metadata from a zero-value instance (Description/Parameters read no
	// fields, so this is safe).
	meta := &ExecTool{}

	return []global.ToolDefinition{
		{
			Name:        "exec",
			Description: meta.Description(),
			RawSchema:   meta.Parameters(),
			Category:    "automation",
			// shell_exec is DefaultEnabled:false → denied by default, so leave
			// DefaultAllow unset.
			DefaultAllow: nil,
			Handler: func(call *global.ToolCall) (*global.Result, error) {
				return tools.ResultToGlobal(real.Execute(call.Ctx, call.Args)), nil
			},
		},
	}
}
