// ClawEh
// License: MIT

package agent

import (
	"os"
	"testing"

	"github.com/PivotLLM/ClawEh/tools"
	toolsfiles "github.com/PivotLLM/ClawEh/tools/files"
	toolssession "github.com/PivotLLM/ClawEh/tools/session"
	toolsshell "github.com/PivotLLM/ClawEh/tools/shell"
)

// TestMain registers the tool providers needed by agent-package tests.
// In production, tool providers are registered by internal/gateway before
// NewAgentLoop is called. Tests that instantiate NewAgentLoop directly
// (e.g. TestAgentLoop_GetStartupInfo) need at least a few providers registered
// so that registerRuntimeTools produces a non-empty tool registry.
func TestMain(m *testing.M) {
	tools.RegisterProvider(tools.NamespacedProvider("file", toolsfiles.GlobalProvider))
	tools.RegisterProvider(tools.NamespacedProvider("shell", toolsshell.GlobalProvider))
	tools.RegisterProvider(tools.NamespacedProvider("session", toolssession.GlobalProvider))
	os.Exit(m.Run())
}
