package internal

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/internal/perms"
	"github.com/PivotLLM/ClawEh/logger"
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
		if err := os.Setenv("CLAW_HOME", detected); err != nil {
			logger.WarnCF("config", "failed to set CLAW_HOME", map[string]any{"path": detected, "error": err.Error()})
		}
		return detected
	}

	home, err := os.UserHomeDir()
	if err != nil {
		logger.WarnCF("config", "cannot determine home directory; using relative .claw", map[string]any{"error": err.Error()})
	}
	return filepath.Join(home, ".claw")
}

// installRoot is the system install location detectInstalledClawHome checks.
// A variable only so tests can point it away from a real /opt/claw.
var installRoot = "/opt/claw"

func detectInstalledClawHome() string {
	// 1. Check if running executable is inside /opt/claw
	if exe, err := os.Executable(); err == nil {
		if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
			exe = resolved
		}
		if strings.HasPrefix(exe, installRoot) {
			return installRoot
		}
	}

	// 2. If the user already has ~/.claw/config.json, respect their user directory
	if userHome, err := os.UserHomeDir(); err == nil && userHome != "" {
		userCfg := filepath.Join(userHome, ".claw", "config.json")
		if fi, err := os.Stat(userCfg); err == nil && !fi.IsDir() {
			return "" // User explicitly has ~/.claw configured
		}
	}

	// 3. Check if /opt/claw/config.json exists and is owned by the current user
	if fi, err := os.Stat(filepath.Join(installRoot, "config.json")); err == nil && !fi.IsDir() {
		// If current user is the owner of /opt/claw, or running as that user
		if stat, ok := fi.Sys().(*syscall.Stat_t); ok {
			curUID := os.Getuid()
			if curUID == int(stat.Uid) {
				return installRoot
			}
		}
	}

	return ""
}

func GetConfigPath() string {
	return filepath.Join(GetClawHome(), "config.json")
}

// LoadConfig seeds and loads the config, and sets the process default
// secrets modes (perms.SetSecretModes) from it, so the secret stores a
// command opens keep security.permissions.secrets.
func LoadConfig() (*config.Config, error) {
	path := GetConfigPath()
	SeedConfig(path)
	cfg, err := config.LoadConfig(path)
	if err != nil {
		return nil, err
	}
	perms.SetSecretModes(cfg.PermissionModes(config.AreaSecrets))
	return cfg, nil
}

// SeedConfig writes a default config to path when no file exists there yet.
// Best-effort: a failure is logged, and loading the path reports the real
// problem.
func SeedConfig(path string) {
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		if mkdirErr := os.MkdirAll(filepath.Dir(path), 0o700); mkdirErr == nil {
			defaultCfg := config.DefaultConfig()
			// Keeps the default_config marker.
			if seedErr := config.SeedDefaultConfig(path, defaultCfg); seedErr != nil {
				logger.WarnCF("config", "failed to seed default config", map[string]any{"path": path, "error": seedErr.Error()})
			}
		}
	}
}
