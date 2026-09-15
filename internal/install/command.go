// Package install provides the `claw install` / `claw uninstall` subcommands,
// which deploy the running binary and register a system boot or user session service
// on Linux (systemd) and macOS (launchd).
package install

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/PivotLLM/ClawEh/app"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/fileutil"
	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/internal"
	"github.com/PivotLLM/ClawEh/internal/network"
)

const (
	serviceName   = "claw"
	openClawAlias = "openclaw"
)

// TargetUser holds the account details under which ClawEh will execute.
type TargetUser struct {
	Username  string
	UID       string
	GID       string
	GroupName string
	HomeDir   string
	IsRoot    bool
}

// NewInstallCommand returns the `claw install` subcommand.
func NewInstallCommand() *cobra.Command {
	var (
		host         string
		port         int
		allowedCIDRs string
		targetUser   string
		customBinDir string
		yes          bool
	)
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install the binary and register a background service that starts " + app.Name() + " at boot or login",
		Long: "Copies the running binary, ensures it is on your PATH, and sets up a background\n" +
			"service to run " + app.Name() + " automatically:\n\n" +
			"  - Run as regular user: Installs in User Mode (user space, no sudo required).\n" +
			"    • Linux: registers a systemd user service (~/.config/systemd/user/claw.service)\n" +
			"    • macOS: registers a launchd LaunchAgent (~/Library/LaunchAgents/com.pivotllm.claweh.plist)\n" +
			"    • Binary copied to ~/bin (if exists) or ~/.local/bin\n\n" +
			"  - Run with sudo: Installs in System Mode (system boot service).\n" +
			"    • Detects invoking user ($SUDO_USER) so " + app.Name() + " never executes as root\n" +
			"    • Linux: registers a systemd system service (/etc/systemd/system/claw.service)\n" +
			"    • macOS: registers a launchd LaunchDaemon (/Library/LaunchDaemons/com.pivotllm.claweh.plist)\n" +
			"    • Binary copied to /usr/local/bin\n\n" +
			"On a headless host, pass --host 0.0.0.0 so the WebUI listens on the network, AND\n" +
			"--allowed-cidrs to define allowed network clients (e.g. --allowed-cidrs 192.168.1.0/24).",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runInstall(host, port, allowedCIDRs, targetUser, customBinDir, yes)
		},
	}
	cmd.Flags().StringVar(&host, "host", "", "Bind address for the web/gateway server (e.g. 0.0.0.0 for all interfaces). Empty keeps the current/seeded value.")
	cmd.Flags().IntVar(&port, "port", 0, "HTTP port for the web/gateway server. 0 keeps the current/seeded value.")
	cmd.Flags().StringVar(&allowedCIDRs, "allowed-cidrs", "",
		"Comma-separated CIDR allowlist for the WebUI/API; loopback is always allowed. "+
			"Empty means loopback only. Give explicit CIDRs (192.168.1.0/24), or a shorthand: "+
			"'private' for the RFC1918 ranges, 'any' for any address. "+
			"Required when --host is not loopback.")
	cmd.Flags().StringVar(&targetUser, "user", "", "Target user account for service execution when running with sudo (defaults to $SUDO_USER).")
	cmd.Flags().StringVar(&customBinDir, "bin-dir", "", "Custom directory to install the binary to (defaults to ~/bin or ~/.local/bin in user mode, /usr/local/bin in system mode).")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Skip interactive confirmation prompt.")
	return cmd
}

// NewUninstallCommand returns the `claw uninstall` subcommand.
func NewUninstallCommand() *cobra.Command {
	var (
		targetUser string
		yes        bool
	)
	cmd := &cobra.Command{
		Use:          "uninstall",
		Short:        "Stop, disable, and remove the background service installed by `claw install`",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runUninstall(targetUser, yes)
		},
	}
	cmd.Flags().StringVar(&targetUser, "user", "", "Target user account when running with sudo (defaults to $SUDO_USER).")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Skip interactive confirmation prompt.")
	return cmd
}

// resolveTargetUser identifies the user account that will own and execute ClawEh.
// When running with sudo/root, it reads $SUDO_USER or explicitUser to guarantee
// that the service never runs as root.
func resolveTargetUser(explicitUser string) (*TargetUser, error) {
	isRoot := os.Geteuid() == 0

	var u *user.User
	var err error

	if isRoot {
		username := explicitUser
		if username == "" {
			username = os.Getenv("SUDO_USER")
		}
		if username == "" || username == "root" {
			return nil, fmt.Errorf(
				"%s must run as a regular user, not root.\n"+
					"When running with sudo, invoke from your normal user account (e.g. `sudo %s install`)\n"+
					"or pass --user <username> to specify the target user explicitly.",
				app.Name(), internal.BinaryName)
		}
		u, err = user.Lookup(username)
		if err != nil {
			return nil, fmt.Errorf("lookup user %q: %w", username, err)
		}
	} else {
		if explicitUser != "" {
			cur, errCur := user.Current()
			if errCur == nil && cur.Username != explicitUser {
				return nil, fmt.Errorf("cannot install for user %q without root privileges", explicitUser)
			}
		}
		u, err = user.Current()
		if err != nil {
			return nil, fmt.Errorf("cannot determine current user: %w", err)
		}
	}

	groupName := u.Gid
	if g, gerr := user.LookupGroupId(u.Gid); gerr == nil {
		groupName = g.Name
	}

	return &TargetUser{
		Username:  u.Username,
		UID:       u.Uid,
		GID:       u.Gid,
		GroupName: groupName,
		HomeDir:   u.HomeDir,
		IsRoot:    isRoot,
	}, nil
}

// resolveBinDir selects the destination directory for the installed binary.
func resolveBinDir(tu *TargetUser, customDir string, existing *ExistingInstall) (string, error) {
	if customDir != "" {
		if err := os.MkdirAll(customDir, 0o755); err != nil {
			return "", fmt.Errorf("creating bin dir %s: %w", customDir, err)
		}
		return customDir, nil
	}

	// If an existing installation binary was detected, preserve that exact directory
	if existing != nil && existing.BinaryPath != "" {
		existingDir := filepath.Dir(existing.BinaryPath)
		if dirExists(existingDir) {
			return existingDir, nil
		}
	}

	// If /opt/claw was configured as ClawHome in existing installation, preserve /opt/claw
	if existing != nil && existing.ClawHome == "/opt/claw" {
		return "/opt/claw", nil
	}

	if tu.IsRoot {
		// System Mode: standard system binary path
		binDir := "/usr/local/bin"
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			return "", fmt.Errorf("creating %s: %w", binDir, err)
		}
		return binDir, nil
	}

	// User Mode: ~/bin if it exists, else ~/.local/bin
	binDir := filepath.Join(tu.HomeDir, "bin")
	if !dirExists(binDir) {
		binDir = filepath.Join(tu.HomeDir, ".local", "bin")
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			return "", fmt.Errorf("creating %s: %w", binDir, err)
		}
	}
	return binDir, nil
}

// confirmPrompt asks the user for confirmation on stdin.
func confirmPrompt(prompt string, autoYes bool) (bool, error) {
	if autoYes {
		return true, nil
	}
	fmt.Printf("%s [y/N]: ", prompt)
	reader := bufio.NewReader(os.Stdin)
	response, err := reader.ReadString('\n')
	if err != nil {
		return false, nil
	}
	response = strings.TrimSpace(strings.ToLower(response))
	return response == "y" || response == "yes", nil
}

func runInstall(host string, port int, allowedCIDRs, targetUser, customBinDir string, autoYes bool) error {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return fmt.Errorf("`%s install` is only supported on Linux and macOS; this is %s", internal.BinaryName, runtime.GOOS)
	}

	tu, err := resolveTargetUser(targetUser)
	if err != nil {
		return err
	}

	// Detect any pre-existing installation or service
	existing := DetectExistingInstall(tu.HomeDir)
	if existing != nil && existing.User != "" && targetUser == "" && tu.IsRoot {
		// Preserve user from existing service
		if preservedUser, pErr := resolveTargetUser(existing.User); pErr == nil {
			tu = preservedUser
		}
	}

	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot locate running binary: %w", err)
	}
	if resolved, rerr := filepath.EvalSymlinks(exePath); rerr == nil {
		exePath = resolved
	}

	binDir, err := resolveBinDir(tu, customBinDir, existing)
	if err != nil {
		return err
	}
	targetBin := filepath.Join(binDir, serviceName)

	// Determine service file path for summary
	var modeName, serviceFilePath string
	if tu.IsRoot {
		modeName = "System Mode (starts at system boot)"
		if runtime.GOOS == "linux" {
			serviceFilePath = systemUnitPath
		} else {
			serviceFilePath = systemLaunchdPath
		}
	} else {
		modeName = "User Mode (user space, no sudo required)"
		if runtime.GOOS == "linux" {
			serviceFilePath = userUnitPath(tu.HomeDir)
		} else {
			serviceFilePath = userLaunchdPath(tu.HomeDir)
		}
	}

	clawHome := resolveClawHome(tu, binDir, existing)

	// Explicitly set CLAW_HOME in process environment so any config access or helper uses clawHome
	_ = os.Setenv(global.EnvVarHome, clawHome)
	_ = os.Setenv("CLAW_HOME", clawHome)

	// 1. Present installation summary and prompt for confirmation
	fmt.Printf("\n%s Installation Summary:\n", app.Name())
	if existing != nil {
		if existing.ServicePath != "" {
			fmt.Printf("  Existing Service: %s (%s)\n", existing.ServicePath, existing.ServiceType)
		}
		if existing.BinaryPath != "" {
			fmt.Printf("  Existing Binary:  %s\n", existing.BinaryPath)
		}
	}
	fmt.Printf("  Platform:        %s (%s)\n", runtime.GOOS, serviceManagerName())
	fmt.Printf("  Mode:            %s\n", modeName)
	fmt.Printf("  Run As User:     %s (UID: %s, GID: %s)\n", tu.Username, tu.UID, tu.GID)
	fmt.Printf("  Home Directory:  %s\n", tu.HomeDir)
	fmt.Printf("  Data Directory:  %s\n", clawHome)
	fmt.Printf("  Target Binary:   %s\n", targetBin)
	fmt.Printf("  Alias Symlink:   %s -> %s\n", filepath.Join(binDir, openClawAlias), serviceName)
	fmt.Printf("  Service File:    %s\n", serviceFilePath)
	fmt.Println()

	confirmed, err := confirmPrompt("Do you want to proceed with installation?", autoYes)
	if err != nil || !confirmed {
		fmt.Println("Installation cancelled.")
		return nil
	}

	// 2. Copy binary into target location (atomic write prevents "text file busy")
	if err := copyBinary(exePath, targetBin); err != nil {
		return fmt.Errorf("copying binary to %s: %w", targetBin, err)
	}
	fixOwnership(targetBin, tu)
	fmt.Printf("Installed binary: %s\n", targetBin)

	// 2b. Symlink openclaw -> claw (for Rabbit R1 ACP connection)
	if err := linkOpenClawAlias(binDir, serviceName); err != nil {
		fmt.Printf("Warning: could not create the openclaw alias (%v).\n"+
			"  The Rabbit R1 spawns `openclaw acp`; without this link it cannot connect.\n", err)
	} else {
		fixOwnership(filepath.Join(binDir, openClawAlias), tu)
		fmt.Printf("Installed alias:  %s -> %s (for the Rabbit R1's `openclaw acp`)\n",
			filepath.Join(binDir, openClawAlias), serviceName)
	}

	// 3. Ensure binDir is on PATH and CLAW_HOME is exported in shell rc
	if note := ensureUserEnv(tu, binDir, clawHome); note != "" {
		fmt.Println(note)
	}

	// 3b. Apply bind settings if provided
	if host != "" || port != 0 {
		if err := applyServerSettings(host, port, clawHome); err != nil {
			return fmt.Errorf("applying server settings: %w", err)
		}
		fixOwnership(filepath.Join(clawHome, "config.json"), tu)
	}

	// 3c. Apply IP allowlist if provided
	if allowedCIDRs == "" && isNetworkBind(host) {
		if existingNet, err := network.CurrentAllowlist(); err == nil && len(existingNet) == 0 {
			return fmt.Errorf(
				"--host %s makes %s listen on the network, but the allowlist is empty, "+
					"so every off-box connection would still be refused.\n"+
					"Pass --allowed-cidrs as well:\n"+
					"  --allowed-cidrs 192.168.1.0/24   your LAN subnet (recommended)\n"+
					"  --allowed-cidrs private          all RFC1918 private ranges\n"+
					"  --allowed-cidrs any              any address — the WebUI has no password yet\n"+
					"Loopback is always allowed, so --host 127.0.0.1 needs none of this",
				host, serviceName)
		}
	}
	if allowedCIDRs != "" {
		if err := applyAllowlist(allowedCIDRs); err != nil {
			return fmt.Errorf("applying allowlist: %w", err)
		}
		fixOwnership(filepath.Join(clawHome, "config.json"), tu)
	}

	// 4. Register and start background service
	if runtime.GOOS == "linux" {
		if err := installSystemd(tu, targetBin, binDir, clawHome); err != nil {
			return fmt.Errorf("installing systemd service: %w", err)
		}
	} else if runtime.GOOS == "darwin" {
		if err := installLaunchd(tu, targetBin, binDir, clawHome); err != nil {
			return fmt.Errorf("installing launchd service: %w", err)
		}
	}
	fixOwnership(filepath.Join(clawHome, "logs"), tu)

	fmt.Println("\nInstalled and running.")
	fmt.Printf("Web interface is at: %s\n\n", accessURL(clawHome))
	fmt.Println("Hints:")
	if runtime.GOOS == "linux" {
		if tu.IsRoot {
			fmt.Printf("  Check status: systemctl status %s\n", serviceName)
		} else {
			fmt.Printf("  Check status: systemctl --user status %s\n", serviceName)
		}
	} else if runtime.GOOS == "darwin" {
		if tu.IsRoot {
			fmt.Printf("  Check status: sudo launchctl list | grep %s\n", launchdLabel)
		} else {
			fmt.Printf("  Check status: launchctl list | grep %s\n", launchdLabel)
		}
	} else {
		fmt.Printf("  Check status: %s status\n", targetBin)
	}
	fmt.Printf("  View logs:    tail -f %s\n", filepath.Join(clawHome, "logs", "claw.log"))
	if tu.IsRoot {
		fmt.Printf("  Uninstall:    sudo %s uninstall\n", targetBin)
	} else {
		fmt.Printf("  Uninstall:    %s uninstall\n", targetBin)
	}
	return nil
}

func runUninstall(targetUser string, autoYes bool) error {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return fmt.Errorf("`%s uninstall` is only supported on Linux and macOS; this is %s", internal.BinaryName, runtime.GOOS)
	}

	tu, err := resolveTargetUser(targetUser)
	if err != nil {
		return err
	}

	existing := DetectExistingInstall(tu.HomeDir)
	if existing != nil && existing.User != "" && targetUser == "" && tu.IsRoot {
		if preservedUser, pErr := resolveTargetUser(existing.User); pErr == nil {
			tu = preservedUser
		}
	}

	dataDirectory := dataDir(tu.HomeDir)
	if existing != nil && existing.ClawHome != "" {
		dataDirectory = existing.ClawHome
	} else if dirExists("/opt/claw") && (tu.IsRoot || tu.Username == "ai") {
		dataDirectory = "/opt/claw"
	}

	var serviceDesc string
	if runtime.GOOS == "linux" {
		if tu.IsRoot {
			serviceDesc = fmt.Sprintf("systemd system service (%s)", systemUnitPath)
		} else {
			serviceDesc = fmt.Sprintf("systemd user service (%s)", userUnitPath(tu.HomeDir))
		}
	} else {
		if tu.IsRoot {
			serviceDesc = fmt.Sprintf("launchd daemon (%s)", systemLaunchdPath)
		} else {
			serviceDesc = fmt.Sprintf("launchd agent (%s)", userLaunchdPath(tu.HomeDir))
		}
	}

	fmt.Printf("\n%s Uninstallation Summary:\n", app.Name())
	fmt.Printf("  Platform:     %s (%s)\n", runtime.GOOS, serviceManagerName())
	fmt.Printf("  Target:       %s\n", serviceDesc)
	fmt.Printf("  Action:       Stop service, disable autostart, and remove service file.\n")
	fmt.Printf("  Note:         Installed binary and data in %s will NOT be deleted.\n\n", dataDirectory)

	confirmed, err := confirmPrompt("Do you want to proceed with removal?", autoYes)
	if err != nil || !confirmed {
		fmt.Println("Uninstallation cancelled.")
		return nil
	}

	if runtime.GOOS == "linux" {
		if err := uninstallSystemd(tu); err != nil {
			return fmt.Errorf("uninstalling systemd service: %w", err)
		}
	} else if runtime.GOOS == "darwin" {
		if err := uninstallLaunchd(tu); err != nil {
			return fmt.Errorf("uninstalling launchd service: %w", err)
		}
	}

	fmt.Printf("\nService removed successfully.\nInstalled binaries and data directory (%s) were left in place.\n", dataDirectory)
	return nil
}

func serviceManagerName() string {
	if runtime.GOOS == "linux" {
		return "systemd"
	}
	return "launchd"
}

// resolveClawHome determines the directory for CLAW_HOME.
// It prioritizes explicit environment variables, detected existing installations,
// /opt/claw if installed there or existing, or the user's ~/.claw directory.
func resolveClawHome(tu *TargetUser, binDir string, existing *ExistingInstall) string {
	if envHome := os.Getenv(global.EnvVarHome); envHome != "" {
		return envHome
	}
	if existing != nil && existing.ClawHome != "" {
		return existing.ClawHome
	}
	if binDir == "/opt/claw" || (existing != nil && strings.HasPrefix(existing.BinaryPath, "/opt/claw")) {
		return "/opt/claw"
	}
	if dirExists("/opt/claw") {
		return "/opt/claw"
	}
	return filepath.Join(tu.HomeDir, global.DefaultDataDir)
}

// buildUnit is preserved for backwards compatibility with tests and callers.
func buildUnit(username, group, execPath, binDir string) string {
	homeDir := filepath.Join("/home", username)
	clawHome := filepath.Join(homeDir, global.DefaultDataDir)
	if envHome := os.Getenv(global.EnvVarHome); envHome != "" {
		clawHome = envHome
	}
	return buildSystemUnit(username, group, execPath, homeDir, binDir, clawHome)
}

// accessURL returns the web UI URL to print after install, derived from the
// active bind host/port. For an all-interfaces bind it uses the host's primary
// private IP so a headless user gets a reachable address, not "0.0.0.0".
func accessURL(optionalClawHome ...string) string {
	host, port := "127.0.0.1", config.DefaultGatewayPort
	cfgPath := internal.GetConfigPath()
	if len(optionalClawHome) > 0 && optionalClawHome[0] != "" {
		cfgPath = filepath.Join(optionalClawHome[0], "config.json")
	}
	if cfg, err := config.LoadConfig(cfgPath); err == nil {
		if cfg.Gateway.Host != "" {
			host = cfg.Gateway.Host
		}
		if cfg.Gateway.Port != 0 {
			port = cfg.Gateway.Port
		}
	}
	switch strings.TrimSpace(host) {
	case "0.0.0.0", "::", "":
		if ip := primaryLANIP(); ip != "" {
			return fmt.Sprintf("http://%s:%d", ip, port)
		}
		return fmt.Sprintf("http://<server-ip>:%d", port)
	case "127.0.0.1", "localhost", "::1":
		return fmt.Sprintf("http://localhost:%d", port)
	default:
		return fmt.Sprintf("http://%s:%d", host, port)
	}
}

// primaryLANIP returns the host's first non-loopback private IPv4, or "".
func primaryLANIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() {
			continue
		}
		if ip4 := ipnet.IP.To4(); ip4 != nil && ip4.IsPrivate() {
			return ip4.String()
		}
	}
	return ""
}

// applyServerSettings writes the requested bind host/port into the config (a
// blank/zero value leaves the existing one untouched), creating the config from
// defaults if it doesn't exist yet. It warns when binding a non-loopback address
// because the WebUI has no authentication.
func applyServerSettings(host string, port int, optionalClawHome ...string) error {
	path := internal.GetConfigPath()
	if len(optionalClawHome) > 0 && optionalClawHome[0] != "" {
		path = filepath.Join(optionalClawHome[0], "config.json")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		return err
	}
	if host != "" {
		cfg.Gateway.Host = host
	}
	if port != 0 {
		cfg.Gateway.Port = port
	}
	if err := config.SaveConfig(path, cfg); err != nil {
		return err
	}
	fmt.Printf("Server bind set to %s:%d (%s)\n", cfg.Gateway.Host, cfg.Gateway.Port, path)
	if isPublicBind(cfg.Gateway.Host) {
		fmt.Printf("Note: %s has no WebUI authentication. Access is restricted to loopback +\n", app.Name())
		fmt.Println("      the private-network IP allowlist (RFC1918). If you widen the allowlist to")
		fmt.Println("      public ranges, put it behind a firewall or an authenticated reverse proxy.")
	}
	return nil
}

// linkOpenClawAlias points <binDir>/openclaw at the installed binary. The link is
// relative so it survives the directory being moved, and is replaced if present.
func linkOpenClawAlias(binDir, target string) error {
	link := filepath.Join(binDir, openClawAlias)
	if err := os.Remove(link); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Symlink(target, link)
}

// isNetworkBind reports whether host makes the gateway listen beyond loopback.
func isNetworkBind(host string) bool {
	switch strings.TrimSpace(host) {
	case "", "127.0.0.1", "localhost", "::1", "[::1]":
		return false
	default:
		return true
	}
}

// applyAllowlist writes a custom IP allowlist (comma-separated CIDRs) into
// gateway.allowed_cidrs in config.json. Each CIDR is validated before saving.
func applyAllowlist(csv string) error {
	cidrs := network.ParseAllowlist(csv)
	path, err := network.ApplyAllowlist(cidrs)
	if err != nil {
		return err
	}
	fmt.Printf("Network allowlist set to %s (loopback always allowed) (%s)\n", network.Describe(cidrs), path)
	return nil
}

// isPublicBind reports whether host exposes the server beyond the local machine.
func isPublicBind(host string) bool {
	switch strings.TrimSpace(host) {
	case "", "127.0.0.1", "localhost", "::1":
		return false
	default:
		return true
	}
}

// servicePATH builds the PATH baked into the systemd unit or launchd plist.
// It prioritizes the target user's local bin directories (~/.local/bin, ~/bin)
// so dev tools (claude code, agy, codex, cursor, etc.) are reachable, the binary
// directory itself (e.g. /opt/claw), any home-relative tool paths (e.g. cargo, nvm),
// and standard clean system directories (/usr/local/bin, /usr/bin, /bin).
// Unneeded sbin and vendor-specific paths (e.g. thinlinc, snap) are excluded.
func servicePATH(homeDir, binDir string) string {
	parts := []string{}
	seen := make(map[string]bool)

	add := func(p string) {
		if p != "" && !seen[p] {
			parts = append(parts, p)
			seen[p] = true
		}
	}

	// 1. Target user's local binary directories if they exist (dev tools: claude, agy, etc.)
	if homeDir != "" {
		localBin := filepath.Join(homeDir, ".local", "bin")
		if dirExists(localBin) {
			add(localBin)
		}
		userBin := filepath.Join(homeDir, "bin")
		if dirExists(userBin) {
			add(userBin)
		}
	}

	// 2. Target binary directory (e.g. /opt/claw)
	if binDir != "" {
		add(binDir)
	}

	// 3. User-specific tool directories from current PATH (e.g. ~/.nvm, ~/.cargo/bin)
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if homeDir != "" && strings.HasPrefix(p, homeDir) {
			add(p)
		} else if homeDir == "" && !isSystemOrSnapPath(p) {
			add(p)
		}
	}

	// 4. Standard clean system binary directories
	for _, p := range []string{"/usr/local/bin", "/usr/bin", "/bin"} {
		add(p)
	}

	return strings.Join(parts, ":")
}

func isSystemOrSnapPath(p string) bool {
	switch p {
	case "/usr/local/sbin", "/usr/sbin", "/sbin", "/snap/bin":
		return true
	default:
		return strings.HasPrefix(p, "/opt/thinl")
	}
}

// ensurePath appends binDir to the user's shell rc if it isn't already on PATH.
// Returns a human-readable note, or "" if PATH already contained binDir.
func ensurePath(binDir string) string {
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if p == binDir {
			return ""
		}
	}

	rc := shellRC()
	const marker = "# Added by claw install"
	if data, err := os.ReadFile(rc); err == nil && strings.Contains(string(data), marker) {
		return fmt.Sprintf("PATH already configured in %s (restart your shell if `%s` isn't found).", rc, internal.BinaryName)
	}

	line := fmt.Sprintf("\n%s\nexport PATH=%q\n", marker, binDir+":$PATH")
	f, err := os.OpenFile(rc, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Sprintf("Could not update %s (%v). Add %s to your PATH manually.", rc, err, binDir)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(line); err != nil {
		return fmt.Sprintf("Could not update %s (%v). Add %s to your PATH manually.", rc, err, binDir)
	}
	return fmt.Sprintf("Added %s to PATH in %s — run `source %s` or open a new terminal.", binDir, rc, rc)
}

// shellRC picks the rc file to update based on the login shell.
func shellRC() string {
	home, _ := os.UserHomeDir()
	switch filepath.Base(os.Getenv("SHELL")) {
	case "zsh":
		return filepath.Join(home, ".zshrc")
	case "bash":
		return filepath.Join(home, ".bashrc")
	default:
		return filepath.Join(home, ".profile")
	}
}

// ensureUserEnv ensures binDir is on PATH and CLAW_HOME is exported in the target user's shell rc.
func ensureUserEnv(tu *TargetUser, binDir, clawHome string) string {
	rc := userShellRC(tu.HomeDir)
	if rc == "" {
		return ""
	}

	data, _ := os.ReadFile(rc)
	content := string(data)

	// Check if binDir needs to be added to PATH
	needPath := true
	if binDir == "/usr/local/bin" || binDir == "/usr/bin" || binDir == "/bin" {
		needPath = false
	} else {
		for _, p := range filepath.SplitList(os.Getenv("PATH")) {
			if p == binDir {
				needPath = false
				break
			}
		}
		if strings.Contains(content, binDir) {
			needPath = false
		}
	}

	// Check if CLAW_HOME needs to be exported
	needClawHome := false
	defaultHome := filepath.Join(tu.HomeDir, global.DefaultDataDir)
	if clawHome != defaultHome {
		if !strings.Contains(content, global.EnvVarHome) {
			needClawHome = true
		}
	}

	if !needPath && !needClawHome {
		return ""
	}

	const marker = "# Added by claw install"
	var lines []string
	var notes []string

	lines = append(lines, "\n"+marker)
	if needPath {
		lines = append(lines, fmt.Sprintf("export PATH=%q", binDir+":$PATH"))
		notes = append(notes, fmt.Sprintf("Added %s to PATH in %s", binDir, rc))
	}
	if needClawHome {
		lines = append(lines, fmt.Sprintf("export %s=%q", global.EnvVarHome, clawHome))
		notes = append(notes, fmt.Sprintf("Exported %s=%s in %s", global.EnvVarHome, clawHome, rc))
	}

	f, err := os.OpenFile(rc, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Sprintf("Could not update %s (%v). Set environment manually.", rc, err)
	}
	defer func() { _ = f.Close() }()

	if _, err := f.WriteString(strings.Join(lines, "\n") + "\n"); err != nil {
		return fmt.Sprintf("Could not update %s (%v). Set environment manually.", rc, err)
	}
	fixOwnership(rc, tu)

	return strings.Join(notes, "\n") + " — run `source " + rc + "` or open a new terminal."
}

func userShellRC(homeDir string) string {
	if homeDir == "" {
		homeDir, _ = os.UserHomeDir()
	}
	switch filepath.Base(os.Getenv("SHELL")) {
	case "zsh":
		return filepath.Join(homeDir, ".zshrc")
	case "bash":
		return filepath.Join(homeDir, ".bashrc")
	default:
		return filepath.Join(homeDir, ".profile")
	}
}

func fixOwnership(path string, tu *TargetUser) {
	if tu == nil || !tu.IsRoot || path == "" {
		return
	}
	uid, err1 := strconv.Atoi(tu.UID)
	gid, err2 := strconv.Atoi(tu.GID)
	if err1 == nil && err2 == nil {
		_ = os.Chown(path, uid, gid)
	}
}

func copyBinary(src, dst string) error {
	if src == dst {
		return nil
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return fileutil.WriteFileAtomic(dst, data, 0o755)
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func dataDir(home string) string {
	if h := os.Getenv(global.EnvVarHome); h != "" {
		return h
	}
	return filepath.Join(home, global.DefaultDataDir)
}

// shellQuote single-quotes s for safe inclusion in shell commands.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}
