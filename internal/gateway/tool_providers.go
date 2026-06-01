package gateway

import (
	"github.com/PivotLLM/ClawEh/pkg/tools"
	"github.com/PivotLLM/ClawEh/pkg/tools/agents"
	"github.com/PivotLLM/ClawEh/pkg/tools/files"
	"github.com/PivotLLM/ClawEh/pkg/tools/hardware"
	"github.com/PivotLLM/ClawEh/pkg/tools/msg"
	"github.com/PivotLLM/ClawEh/pkg/tools/schedule"
	"github.com/PivotLLM/ClawEh/pkg/tools/session"
	"github.com/PivotLLM/ClawEh/pkg/tools/shell"
	"github.com/PivotLLM/ClawEh/pkg/tools/skills"
	toolsweb "github.com/PivotLLM/ClawEh/pkg/tools/web"
)

// registerToolProviders registers all tool providers in the global registry.
// Must be called from setupAndStartServices before agent loop initialization.
func registerToolProviders() {
	tools.RegisterProvider(files.Provider)
	tools.RegisterProvider(toolsweb.Provider)
	tools.RegisterProvider(session.Provider)
	tools.RegisterProvider(shell.Provider)
	tools.RegisterProvider(skills.Provider)
	tools.RegisterProvider(agents.Provider)
	tools.RegisterProvider(hardware.Provider)
	tools.RegisterProvider(schedule.Provider)
	tools.RegisterProvider(msg.Provider)
}

// RegisterToolProvidersForTest is exported for use in tests that need
// providers registered without starting the full gateway.
// Safe to call multiple times — RegisterProvider is idempotent.
func RegisterToolProvidersForTest() {
	registerToolProviders()
}
