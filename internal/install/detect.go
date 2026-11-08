package install

import (
	"bufio"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/utils"
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

// optClawHome is the data directory of the standard system install. Its
// binary and the openclaw alias live in its bin area, optClawBinDir.
const optClawHome = "/opt/claw"

// optClawBinDir is where an /opt/claw install keeps the binary and the alias.
var optClawBinDir = filepath.Join(optClawHome, config.BinDirName)

// inOptClaw reports whether p is /opt/claw or a path inside it (not a
// sibling such as /opt/claw-old).
func inOptClaw(p string) bool {
	return p == optClawHome || strings.HasPrefix(p, optClawHome+"/")
}

// host is the machine the installer inspects. Every path the installer works
// with is the machine's own (/opt/claw, /etc/systemd/system/...); root, when
// set, is prefixed to them on disk, so tests build a fake install in a temp
// dir. The command hooks keep tests from consulting the real system.
type host struct {
	root       string
	executable func() (string, error)
	lookPath   func(file string) (string, error)
	commandOK  func(name string, args ...string) bool
}

// realHost is the machine the installer runs on.
func realHost() *host {
	return &host{
		executable: os.Executable,
		lookPath:   exec.LookPath,
		commandOK: func(name string, args ...string) bool {
			return exec.Command(name, args...).Run() == nil //nolint:gosec // fixed systemctl binary and the installer's own unit name
		},
	}
}

// path maps one of the machine's paths to where it lies on disk.
func (h *host) path(p string) string {
	if h.root == "" {
		return p
	}
	return filepath.Join(h.root, p)
}

func (h *host) fileExists(p string) bool { return fileExists(h.path(p)) }

func (h *host) dirExists(p string) bool { return dirExists(h.path(p)) }

// exists reports whether anything, a dangling symlink included, is at p.
func (h *host) exists(p string) bool {
	_, err := os.Lstat(h.path(p))
	return err == nil
}

// ownerName is the name of the account owning p, or "" for root or when it
// cannot be found.
func (h *host) ownerName(p string) string {
	fi, err := os.Stat(h.path(p))
	if err != nil {
		return ""
	}
	stat, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	u, err := user.LookupId(strconv.FormatUint(uint64(stat.Uid), 10))
	if err != nil || u.Username == "root" {
		return ""
	}
	return u.Username
}

// optClawBinary is the binary of an /opt/claw install: bin/claw, else the
// older /opt/claw/claw, else "".
func (h *host) optClawBinary() string {
	for _, p := range []string{filepath.Join(optClawBinDir, serviceName), filepath.Join(optClawHome, serviceName)} {
		if h.fileExists(p) {
			return p
		}
	}
	return ""
}

// leftoverOptClawFiles lists the binary and alias an older /opt/claw install
// kept directly in the data directory, which are no longer used once binDir
// is optClawBinDir.
func (h *host) leftoverOptClawFiles(binDir string) []string {
	if binDir != optClawBinDir {
		return nil
	}
	var out []string
	for _, name := range []string{serviceName, openClawAlias} {
		if p := filepath.Join(optClawHome, name); h.exists(p) {
			out = append(out, p)
		}
	}
	return out
}

// DetectExistingInstall searches for an existing ClawEh installation on the system.
// It inspects systemd unit files, launchd plists, running process paths, and PATH.
func DetectExistingInstall(homeDir string) *ExistingInstall {
	h := realHost()
	switch runtime.GOOS {
	case "linux":
		return h.detectLinuxInstall(homeDir)
	case "darwin":
		return h.detectDarwinInstall(homeDir)
	}
	return nil
}

func (h *host) detectLinuxInstall(homeDir string) *ExistingInstall {
	// 1. Check systemd system service (/etc/systemd/system/claw.service)
	if h.fileExists(systemUnitPath) {
		inst := h.parseSystemdUnit(systemUnitPath, "systemd-system")
		inst.IsActive = h.commandOK("systemctl", "is-active", "--quiet", serviceName)
		return inst
	}

	// 2. Check systemd user service (~/.config/systemd/user/claw.service)
	userPath := userUnitPath(homeDir)
	if h.fileExists(userPath) {
		inst := h.parseSystemdUnit(userPath, "systemd-user")
		inst.IsActive = h.commandOK("systemctl", "--user", "is-active", "--quiet", serviceName)
		return inst
	}

	// 3. Check /opt/claw (standard production / system install location)
	if h.dirExists(optClawHome) {
		return &ExistingInstall{
			ClawHome:   optClawHome,
			BinaryPath: h.optClawBinary(),
			User:       h.ownerName(optClawHome),
		}
	}

	// 4. Check running executable, then the system PATH
	return h.detectFromBinary()
}

func (h *host) detectDarwinInstall(homeDir string) *ExistingInstall {
	// 1. Check LaunchDaemon (/Library/LaunchDaemons/com.pivotllm.claweh.plist)
	if h.fileExists(systemLaunchdPath) {
		inst := h.parseLaunchdPlist(systemLaunchdPath, "launchd-daemon")
		inst.IsActive = isLaunchdServiceActive(launchdLabel)
		return inst
	}

	// 2. Check LaunchAgent (~/Library/LaunchAgents/com.pivotllm.claweh.plist)
	userPath := userLaunchdPath(homeDir)
	if h.fileExists(userPath) {
		inst := h.parseLaunchdPlist(userPath, "launchd-agent")
		inst.IsActive = isLaunchdServiceActive(launchdLabel)
		return inst
	}

	// 3. Check /opt/claw
	if h.dirExists(optClawHome) {
		return &ExistingInstall{
			ClawHome:   optClawHome,
			BinaryPath: h.optClawBinary(),
		}
	}

	// 4. Check running executable, then the system PATH
	return h.detectFromBinary()
}

// detectFromBinary finds an install by the running executable, else by claw
// on PATH, ignoring build and temporary locations.
func (h *host) detectFromBinary() *ExistingInstall {
	candidates := []func() (string, error){
		h.executable,
		func() (string, error) { return h.lookPath(serviceName) },
	}
	for _, find := range candidates {
		p, err := find()
		if err != nil {
			continue
		}
		resolved, err := filepath.EvalSymlinks(p)
		if err != nil || isBuildOrTempPath(resolved) {
			continue
		}
		inst := &ExistingInstall{BinaryPath: resolved}
		if inOptClaw(resolved) {
			inst.ClawHome = optClawHome
		}
		return inst
	}
	return nil
}

func (h *host) parseSystemdUnit(path, serviceType string) *ExistingInstall {
	inst := &ExistingInstall{
		ServicePath: path,
		ServiceType: serviceType,
	}

	f, err := os.Open(h.path(path))
	if err != nil {
		return inst
	}
	defer utils.CloseQuietly(f)

	var workingDir, pathEnv string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if after, ok := strings.CutPrefix(line, "ExecStart="); ok {
			val := after
			parts := strings.Fields(val)
			if len(parts) > 0 {
				inst.BinaryPath = parts[0]
			}
		} else if after, ok := strings.CutPrefix(line, "User="); ok {
			inst.User = after
		} else if after, ok := strings.CutPrefix(line, "WorkingDirectory="); ok {
			workingDir = strings.Trim(after, "\"' \t")
		} else if after, ok := strings.CutPrefix(line, "Environment="); ok {
			envVal := after
			if _, after, ok := strings.Cut(envVal, "CLAW_HOME="); ok {
				sub := after
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
			if _, after, ok := strings.Cut(envVal, "PATH="); ok {
				pathEnv = after
			}
		}
	}

	// Fallback inference for ClawHome
	if inst.ClawHome == "" {
		if inOptClaw(inst.BinaryPath) {
			inst.ClawHome = "/opt/claw"
		} else if workingDir != "" && workingDir != "/" && !strings.HasPrefix(workingDir, "/home") {
			inst.ClawHome = workingDir
		} else if pathHasOptClaw(pathEnv) {
			inst.ClawHome = "/opt/claw"
		} else if h.dirExists(optClawHome) {
			inst.ClawHome = optClawHome
		}
	}

	// Fallback inference for User
	if inst.User == "" && inst.ClawHome != "" {
		inst.User = h.ownerName(inst.ClawHome)
	}
	if inst.User == "" && inst.BinaryPath != "" {
		inst.User = h.ownerName(inst.BinaryPath)
	}

	return inst
}

// pathHasOptClaw reports whether a unit's PATH value lists a folder in
// /opt/claw.
func pathHasOptClaw(pathEnv string) bool {
	dirs := strings.FieldsFunc(pathEnv, func(r rune) bool {
		return r == ':' || r == '"' || r == '\'' || r == ' ' || r == '\t'
	})
	return slices.ContainsFunc(dirs, inOptClaw)
}

func (h *host) parseLaunchdPlist(path, serviceType string) *ExistingInstall {
	inst := &ExistingInstall{
		ServicePath: path,
		ServiceType: serviceType,
	}

	data, err := os.ReadFile(h.path(path))
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
		if inOptClaw(inst.BinaryPath) {
			inst.ClawHome = "/opt/claw"
		} else if h.dirExists(optClawHome) {
			inst.ClawHome = optClawHome
		}
	}

	return inst
}

// isBuildOrTempPath returns true if the path appears to be a build artifact or temp file.
func isBuildOrTempPath(p string) bool {
	clean := filepath.Clean(p)
	parts := strings.SplitSeq(clean, string(filepath.Separator))
	for part := range parts {
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
