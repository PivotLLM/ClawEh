package install

import (
	"bufio"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
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

	// 3. Check /opt/claw (standard production / system install location)
	if dirExists("/opt/claw") {
		inst := &ExistingInstall{
			ClawHome: "/opt/claw",
		}
		if fileExists("/opt/claw/claw") {
			inst.BinaryPath = "/opt/claw/claw"
		}
		if fi, err := os.Stat("/opt/claw"); err == nil {
			if stat, ok := fi.Sys().(*syscall.Stat_t); ok {
				if u, uErr := user.LookupId(strconv.FormatUint(uint64(stat.Uid), 10)); uErr == nil && u.Username != "root" {
					inst.User = u.Username
				}
			}
		}
		return inst
	}

	// 4. Check running executable if not a temporary or build directory
	if exePath, err := os.Executable(); err == nil {
		if resolved, rerr := filepath.EvalSymlinks(exePath); rerr == nil {
			if !isBuildOrTempPath(resolved) {
				inst := &ExistingInstall{BinaryPath: resolved}
				if strings.HasPrefix(resolved, "/opt/claw") {
					inst.ClawHome = "/opt/claw"
				}
				return inst
			}
		}
	}

	// 5. Check system PATH
	if p, err := exec.LookPath(serviceName); err == nil {
		if resolved, rerr := filepath.EvalSymlinks(p); rerr == nil {
			if !isBuildOrTempPath(resolved) {
				inst := &ExistingInstall{BinaryPath: resolved}
				if strings.HasPrefix(resolved, "/opt/claw") {
					inst.ClawHome = "/opt/claw"
				}
				return inst
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

	// 3. Check /opt/claw
	if dirExists("/opt/claw") {
		inst := &ExistingInstall{
			ClawHome: "/opt/claw",
		}
		if fileExists("/opt/claw/claw") || fileExists("/opt/claw/bin/claw") {
			if fileExists("/opt/claw/claw") {
				inst.BinaryPath = "/opt/claw/claw"
			} else {
				inst.BinaryPath = "/opt/claw/bin/claw"
			}
		}
		return inst
	}

	// 4. Check running executable
	if exePath, err := os.Executable(); err == nil {
		if resolved, rerr := filepath.EvalSymlinks(exePath); rerr == nil {
			if !isBuildOrTempPath(resolved) {
				inst := &ExistingInstall{BinaryPath: resolved}
				if strings.HasPrefix(resolved, "/opt/claw") {
					inst.ClawHome = "/opt/claw"
				}
				return inst
			}
		}
	}

	// 5. Check system PATH
	if p, err := exec.LookPath(serviceName); err == nil {
		if resolved, rerr := filepath.EvalSymlinks(p); rerr == nil {
			if !isBuildOrTempPath(resolved) {
				inst := &ExistingInstall{BinaryPath: resolved}
				if strings.HasPrefix(resolved, "/opt/claw") {
					inst.ClawHome = "/opt/claw"
				}
				return inst
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

	var workingDir, pathEnv string
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
		} else if strings.HasPrefix(line, "WorkingDirectory=") {
			workingDir = strings.Trim(strings.TrimPrefix(line, "WorkingDirectory="), "\"' \t")
		} else if strings.HasPrefix(line, "Environment=") {
			envVal := strings.TrimPrefix(line, "Environment=")
			if idx := strings.Index(envVal, "CLAW_HOME="); idx != -1 {
				sub := envVal[idx+len("CLAW_HOME="):]
				if len(sub) > 0 && (sub[0] == '"' || sub[0] == '\'') {
					q := sub[0]
					if end := strings.IndexByte(sub[1:], q); end != -1 {
						inst.ClawHome = sub[1 : 1+end]
					} else {
						inst.ClawHome = strings.Trim(sub, "\"' \t")
					}
				} else {
					fields := strings.Fields(sub)
					if len(fields) > 0 {
						inst.ClawHome = strings.Trim(fields[0], "\"' \t")
					}
				}
			}
			if idx := strings.Index(envVal, "PATH="); idx != -1 {
				pathEnv = envVal[idx+len("PATH="):]
			}
		}
	}

	// Fallback inference for ClawHome
	if inst.ClawHome == "" {
		if strings.HasPrefix(inst.BinaryPath, "/opt/claw") {
			inst.ClawHome = "/opt/claw"
		} else if workingDir != "" && workingDir != "/" && !strings.HasPrefix(workingDir, "/home") {
			inst.ClawHome = workingDir
		} else if strings.Contains(pathEnv, "/opt/claw") {
			inst.ClawHome = "/opt/claw"
		} else if dirExists("/opt/claw") {
			inst.ClawHome = "/opt/claw"
		}
	}

	// Fallback inference for User
	if inst.User == "" && inst.ClawHome != "" {
		if fi, err := os.Stat(inst.ClawHome); err == nil {
			if stat, ok := fi.Sys().(*syscall.Stat_t); ok {
				if u, uErr := user.LookupId(strconv.FormatUint(uint64(stat.Uid), 10)); uErr == nil && u.Username != "root" {
					inst.User = u.Username
				}
			}
		}
	}
	if inst.User == "" && inst.BinaryPath != "" {
		if fi, err := os.Stat(inst.BinaryPath); err == nil {
			if stat, ok := fi.Sys().(*syscall.Stat_t); ok {
				if u, uErr := user.LookupId(strconv.FormatUint(uint64(stat.Uid), 10)); uErr == nil && u.Username != "root" {
					inst.User = u.Username
				}
			}
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

	// Fallback inference for ClawHome
	if inst.ClawHome == "" {
		if strings.HasPrefix(inst.BinaryPath, "/opt/claw") {
			inst.ClawHome = "/opt/claw"
		} else if dirExists("/opt/claw") {
			inst.ClawHome = "/opt/claw"
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
