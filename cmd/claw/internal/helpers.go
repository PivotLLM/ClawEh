package internal

import (
	"os"
	"path/filepath"

	"github.com/PivotLLM/ClawEh/pkg/config"
)

// BinaryName is set from main() to filepath.Base(os.Args[0]).
var BinaryName = "claw"

// GetPicoclawHome returns the claw home directory.
// Priority: $CLAW_HOME > ~/.claw
func GetPicoclawHome() string {
	if home := os.Getenv("CLAW_HOME"); home != "" {
		return home
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claw")
}

func GetConfigPath() string {
	return filepath.Join(GetPicoclawHome(), "config.json")
}

func LoadConfig() (*config.Config, error) {
	path := GetConfigPath()
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		if mkdirErr := os.MkdirAll(filepath.Dir(path), 0o755); mkdirErr == nil {
			defaultCfg := config.DefaultConfig()
			_ = config.SaveConfig(path, defaultCfg) // best-effort
		}
	}
	return config.LoadConfig(path)
}

// FormatVersion returns the version string with optional git commit
// Deprecated: Use pkg/config.FormatVersion instead
func FormatVersion() string {
	return config.FormatVersion()
}

// FormatBuildInfo returns build time and go version info
// Deprecated: Use pkg/config.FormatBuildInfo instead
func FormatBuildInfo() (string, string) {
	return config.FormatBuildInfo()
}

// GetVersion returns the version string
// Deprecated: Use pkg/config.GetVersion instead
func GetVersion() string {
	return config.GetVersion()
}
