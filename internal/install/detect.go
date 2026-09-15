package install

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// ExistingInstall holds details of a previously installed ClawEh service or binary.
type ExistingInstall struct {
	BinaryPath  string
	ServiceType string // "systemd-system", "systemd-user", "launchd-daemon", "launchd-agent"
	ServicePath string
	User        string
	ClawHome    string
	IsActive    bool
}

// DetectExistingInstall searches for an existing ClawEh installation on the system.
// It inspects systemd unit files, launchd plists, running process paths, and PATH.
func DetectExistingInstall(homeDir string) *ExistingInstall {
	if runtime.GOOS == "linux" {
		return detectLinuxInstall(homeDir)
	} else if runtime.GOOS == "darwin" {
		return detectDarwinInstall(homeDir)
	}
	return nil
}

func detectLinuxInstall(homeDir string) *ExistingInstall {
	// 1. Check systemd system service (/etc/systemd/system/claw.service)
	if fileExists(systemUnitPath) {
		inst := parseSystemdUnit(systemUnitPath, "systemd-system")
		inst.IsActive = exec.Command("systemctl", "is-active", "--quiet", serviceName).Run() == nil
		return inst
	}

	// 2. Check systemd user service (~/.config/systemd/user/claw.service)
	userPath := userUnitPath(homeDir)
	if fileExists(userPath) {
		inst := parseSystemdUnit(userPath, "systemd-user")
		inst.IsActive = exec.Command("systemctl", "--user", "is-active", "--quiet", serviceName).Run() == nil
		return inst
	}

	// 3. Check running executable if not a temporary or build directory
	if exePath, err := os.Executable(); err == nil {
		if resolved, rerr := filepath.EvalSymlinks(exePath); rerr == nil {
			if !isBuildOrTempPath(resolved) {
				return &ExistingInstall{BinaryPath: resolved}
			}
		}
	}

	// 4. Check system PATH
	if p, err := exec.LookPath(serviceName); err == nil {
		if resolved, rerr := filepath.EvalSymlinks(p); rerr == nil {
			if !isBuildOrTempPath(resolved) {
				return &ExistingInstall{BinaryPath: resolved}
			}
		}
	}

	return nil
}

func detectDarwinInstall(homeDir string) *ExistingInstall {
	// 1. Check LaunchDaemon (/Library/LaunchDaemons/com.pivotllm.claweh.plist)
	if fileExists(systemLaunchdPath) {
		inst := parseLaunchdPlist(systemLaunchdPath, "launchd-daemon")
		inst.IsActive = isLaunchdServiceActive(launchdLabel)
		return inst
	}

	// 2. Check LaunchAgent (~/Library/LaunchAgents/com.pivotllm.claweh.plist)
	userPath := userLaunchdPath(homeDir)
	if fileExists(userPath) {
		inst := parseLaunchdPlist(userPath, "launchd-agent")
		inst.IsActive = isLaunchdServiceActive(launchdLabel)
		return inst
	}

	// 3. Check running executable
	if exePath, err := os.Executable(); err == nil {
		if resolved, rerr := filepath.EvalSymlinks(exePath); rerr == nil {
			if !isBuildOrTempPath(resolved) {
				return &ExistingInstall{BinaryPath: resolved}
			}
		}
	}

	// 4. Check system PATH
	if p, err := exec.LookPath(serviceName); err == nil {
		if resolved, rerr := filepath.EvalSymlinks(p); rerr == nil {
			if !isBuildOrTempPath(resolved) {
				return &ExistingInstall{BinaryPath: resolved}
			}
		}
	}

	return nil
}

func parseSystemdUnit(path, serviceType string) *ExistingInstall {
	inst := &ExistingInstall{
		ServicePath: path,
		ServiceType: serviceType,
	}

	f, err := os.Open(path)
	if err != nil {
		return inst
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "ExecStart=") {
			val := strings.TrimPrefix(line, "ExecStart=")
			parts := strings.Fields(val)
			if len(parts) > 0 {
				inst.BinaryPath = parts[0]
			}
		} else if strings.HasPrefix(line, "User=") {
			inst.User = strings.TrimPrefix(line, "User=")
		} else if strings.HasPrefix(line, "Environment=CLAW_HOME=") {
			inst.ClawHome = strings.TrimPrefix(line, "Environment=CLAW_HOME=")
		}
	}
	return inst
}

func parseLaunchdPlist(path, serviceType string) *ExistingInstall {
	inst := &ExistingInstall{
		ServicePath: path,
		ServiceType: serviceType,
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return inst
	}
	content := string(data)

	// Extract binary from ProgramArguments
	if idx := strings.Index(content, "<key>ProgramArguments</key>"); idx != -1 {
		sub := content[idx:]
		if start := strings.Index(sub, "<string>"); start != -1 {
			if end := strings.Index(sub[start+8:], "</string>"); end != -1 {
				inst.BinaryPath = sub[start+8 : start+8+end]
			}
		}
	}

	// Extract UserName
	if idx := strings.Index(content, "<key>UserName</key>"); idx != -1 {
		sub := content[idx:]
		if start := strings.Index(sub, "<string>"); start != -1 {
			if end := strings.Index(sub[start+8:], "</string>"); end != -1 {
				inst.User = sub[start+8 : start+8+end]
			}
		}
	}

	// Extract CLAW_HOME
	if idx := strings.Index(content, "<key>CLAW_HOME</key>"); idx != -1 {
		sub := content[idx:]
		if start := strings.Index(sub, "<string>"); start != -1 {
			if end := strings.Index(sub[start+8:], "</string>"); end != -1 {
				inst.ClawHome = sub[start+8 : start+8+end]
			}
		}
	}

	return inst
}

// isBuildOrTempPath returns true if the path appears to be a build artifact or temp file.
func isBuildOrTempPath(p string) bool {
	clean := filepath.Clean(p)
	parts := strings.Split(clean, string(filepath.Separator))
	for _, part := range parts {
		if part == "build" || part == "tmp" || part == "temp" || strings.HasPrefix(part, "claw-upgrade-") {
			return true
		}
	}
	return false
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}
