// ClawEh
// License: MIT

package admincmd

import (
	"os"
	"path/filepath"

	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/internal/install"
)

// ResolveHome returns the CLAW_HOME the running service uses, so the
// credentials file lands where the gateway will read it: the CLAW_HOME
// environment variable, else the CLAW_HOME of the installed service
// definition (systemd unit or launchd plist), else ~/.claw. The detected
// install, when there is one, is returned too so a root caller can hand the
// file to the service account.
func ResolveHome() (string, *install.ExistingInstall) {
	userHome, err := os.UserHomeDir()
	if err != nil {
		userHome = "" // no home directory: install detection and the ~/.claw fallback go without it
	}
	inst := install.DetectExistingInstall(userHome)
	if home := os.Getenv(global.EnvVarHome); home != "" {
		return home, inst
	}
	if inst != nil && inst.ClawHome != "" {
		return inst.ClawHome, inst
	}
	return filepath.Join(userHome, global.DefaultDataDir), inst
}
