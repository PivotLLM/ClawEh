package internal

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/PivotLLM/ClawEh/config"
)

// BinaryName is set from main() to filepath.Base(os.Args[0]).
var BinaryName = "claw"

// GetClawHome returns the claw home directory.
// Priority:
// 1. $CLAW_HOME environment variable
// 2. Existing service definition (systemd unit or launchd plist)
// 3. /opt/claw if running from /opt/claw, if /opt/claw/config.json exists, or if /opt/claw exists and ~/.claw does not
// 4. ~/.claw (default data directory)
func GetClawHome() string {
	if home := os.Getenv("CLAW_HOME"); home != "" {
		return home
	}

	if detected := detectInstalledClawHome(); detected != "" {
		_ = os.Setenv("CLAW_HOME", detected)
		return detected
	}

	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claw")
}

func detectInstalledClawHome() string {
	// 1. Check if running executable is inside /opt/claw
	if exe, err := os.Executable(); err == nil {
		if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
			exe = resolved
		}
		if strings.HasPrefix(exe, "/opt/claw") {
			return "/opt/claw"
		}
	}

	// 2. If the user already has ~/.claw/config.json, respect their user directory
	userHome, _ := os.UserHomeDir()
	if userHome != "" {
		userCfg := filepath.Join(userHome, ".claw", "config.json")
		if fi, err := os.Stat(userCfg); err == nil && !fi.IsDir() {
			return "" // User explicitly has ~/.claw configured
		}
	}

	// 3. Check if /opt/claw/config.json exists and is owned by the current user
	if fi, err := os.Stat("/opt/claw/config.json"); err == nil && !fi.IsDir() {
		// If current user is the owner of /opt/claw, or running as that user
		if stat, ok := fi.Sys().(*syscall.Stat_t); ok {
			curUID := os.Getuid()
			if curUID == int(stat.Uid) {
				return "/opt/claw"
			}
		}
	}

	return ""
}

func GetConfigPath() string {
	return filepath.Join(GetClawHome(), "config.json")
}

func LoadConfig() (*config.Config, error) {
	path := GetConfigPath()
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		if mkdirErr := os.MkdirAll(filepath.Dir(path), 0o755); mkdirErr == nil {
			defaultCfg := config.DefaultConfig()
			_ = config.SeedDefaultConfig(path, defaultCfg) // best-effort; keeps default_config marker
		}
	}
	return config.LoadConfig(path)
}
