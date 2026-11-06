// ClawEh
// License: MIT

package admincmd

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/PivotLLM/ClawEh/config"
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

// SecretsFileMode is security.permissions.secrets.file_mode from the config
// file in home (its built-in default when there is none), the mode the
// credentials file is written with. Only that block is read: the rest of the
// config may reference secrets this shell cannot resolve.
func SecretsFileMode(home string) (fs.FileMode, error) {
	p, err := config.ReadPermissions(filepath.Join(home, "config.json"))
	if err != nil {
		return 0, fmt.Errorf("read security.permissions from %s: %w", filepath.Join(home, "config.json"), err)
	}
	return p.Modes(config.AreaSecrets).File, nil
}
