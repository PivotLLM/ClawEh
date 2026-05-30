// ClawEh
// License: MIT

package agent

import (
	"os"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/tools"
	toolsfiles "github.com/PivotLLM/ClawEh/pkg/tools/files"
	toolsshell "github.com/PivotLLM/ClawEh/pkg/tools/shell"
	toolssession "github.com/PivotLLM/ClawEh/pkg/tools/session"
)

// TestMain registers the tool providers needed by agent-package tests.
// In production, tool providers are registered by internal/gateway before
// NewAgentLoop is called. Tests that instantiate NewAgentLoop directly
// (e.g. TestAgentLoop_GetStartupInfo) need at least a few providers registered
// so that registerRuntimeTools produces a non-empty tool registry.
func TestMain(m *testing.M) {
	tools.RegisterProvider(toolsfiles.Provider)
	tools.RegisterProvider(toolsshell.Provider)
	tools.RegisterProvider(toolssession.Provider)
	os.Exit(m.Run())
}
