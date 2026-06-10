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
	tools.RegisterProvider(tools.NamespacedProvider("file", files.GlobalProvider))
	tools.RegisterProvider(tools.NamespacedProvider("web", toolsweb.GlobalProvider))
	tools.RegisterProvider(tools.NamespacedProvider("session", session.GlobalProvider))
	tools.RegisterProvider(tools.NamespacedProvider("shell", shell.GlobalProvider))
	tools.RegisterProvider(tools.NamespacedProvider("skill", skills.GlobalProvider))
	tools.RegisterProvider(tools.NamespacedProvider("agent", agents.GlobalProvider))
	tools.RegisterProvider(tools.NamespacedProvider("hw", hardware.GlobalProvider))
	tools.RegisterProvider(tools.NamespacedProvider("msg", msg.GlobalProvider))
	// schedule stays catalogue-only: the cron tool is a runtime tool registered
	// directly via agentLoop.RegisterTool (renamed to cron_schedule).
	tools.RegisterProvider(schedule.Provider)
}

// RegisterToolProvidersForTest is exported for use in tests that need
// providers registered without starting the full gateway.
// Safe to call multiple times — RegisterProvider is idempotent.
func RegisterToolProvidersForTest() {
	registerToolProviders()
}
