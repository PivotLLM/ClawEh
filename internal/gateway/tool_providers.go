package gateway

import (
	"github.com/PivotLLM/ClawEh/tools"
	"github.com/PivotLLM/ClawEh/tools/agents"
	cogmem "github.com/PivotLLM/ClawEh/tools/cogmem"
	common "github.com/PivotLLM/ClawEh/tools/common"
	"github.com/PivotLLM/ClawEh/tools/files"
	fusion "github.com/PivotLLM/ClawEh/tools/fusion"
	maestro "github.com/PivotLLM/ClawEh/tools/maestro"
	"github.com/PivotLLM/ClawEh/tools/msg"
	"github.com/PivotLLM/ClawEh/tools/schedule"
	"github.com/PivotLLM/ClawEh/tools/session"
	"github.com/PivotLLM/ClawEh/tools/shell"
	"github.com/PivotLLM/ClawEh/tools/skills"
	"github.com/PivotLLM/ClawEh/tools/timetool"
	toolsweb "github.com/PivotLLM/ClawEh/tools/web"
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
	tools.RegisterProvider(tools.NamespacedProvider("msg", msg.GlobalProvider))
	tools.RegisterProvider(tools.NamespacedProvider("cogmem", cogmem.GlobalProvider))
	tools.RegisterProvider(tools.NamespacedProvider("common", common.GlobalProvider))
	tools.RegisterProvider(tools.NamespacedProvider("time", timetool.GlobalProvider))
	tools.RegisterProvider(tools.NamespacedProvider("maestro", maestro.GlobalProvider))
	// Bare names: fusion tool names are already service-prefixed (e.g.
	// microsoft365_mail_read_inbox), so publish them without a "fusion_" prefix.
	// "fusion" still identifies the per-agent suite toggle. Service configs must
	// keep their tool names globally unique.
	tools.RegisterProvider(tools.BareNamespacedProvider("fusion", fusion.GlobalProvider))
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
