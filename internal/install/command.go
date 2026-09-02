// Package install provides the `claw install` / `claw uninstall` subcommands,
// which deploy the running binary and register a systemd system service that
// runs ClawEh as the invoking user at boot.
package install

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
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
	serviceName = "claw"
	unitPath    = "/etc/systemd/system/claw.service"
)

// NewInstallCommand returns the `claw install` subcommand.
func NewInstallCommand() *cobra.Command {
	var host string
	var port int
	var allowedCIDRs string
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install the binary and register a systemd service that starts " + app.Name() + " at boot",
		Long: "Copies the running binary to ~/bin (or ~/.local/bin), ensures that directory is on\n" +
			"your PATH, and writes a systemd system service that runs " + app.Name() + " as your user\n" +
			"account at boot. Writing the service unit requires sudo; you'll be prompted for your\n" +
			"password. Run this as your normal user, not with sudo.\n\n" +
			"On a headless host, pass --host 0.0.0.0 so the WebUI listens on the network, AND\n" +
			"--allowed-cidrs to say who may reach it — binding alone is not enough. The WebUI has\n" +
			"no authentication, so access defaults to loopback only; without an allowlist a\n" +
			"network client is refused even when the port is open. Example:\n" +
			"  --host 0.0.0.0 --allowed-cidrs 192.168.1.0/24\n" +
			"Use '*' to allow any address (understand what that exposes first). Note 0.0.0.0/0\n" +
			"is an IPv4 prefix and still refuses IPv6 clients; '*' covers both families.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runInstall(host, port, allowedCIDRs)
		},
	}
	cmd.Flags().StringVar(&host, "host", "", "Bind address for the web/gateway server (e.g. 0.0.0.0 for all interfaces). Empty keeps the current/seeded value.")
	cmd.Flags().IntVar(&port, "port", 0, "HTTP port for the web/gateway server. 0 keeps the current/seeded value.")
	cmd.Flags().StringVar(&allowedCIDRs, "allowed-cidrs", "",
		"Comma-separated CIDR allowlist for the WebUI/API; loopback is always allowed. "+
			"Empty means loopback only. Give explicit CIDRs (192.168.1.0/24), or a shorthand: "+
			"'private' for the RFC1918 ranges, 'any' for any address. "+
			"Required when --host is not loopback.")
	return cmd
}

// NewUninstallCommand returns the `claw uninstall` subcommand.
func NewUninstallCommand() *cobra.Command {
	return &cobra.Command{
		Use:          "uninstall",
		Short:        "Stop and remove the systemd service installed by `claw install`",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runUninstall()
		},
	}
}

func runInstall(host string, port int, allowedCIDRs string) error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("`%s install` is only supported on Linux (systemd); this is %s", serviceName, runtime.GOOS)
	}
	if os.Geteuid() == 0 {
		return fmt.Errorf("run `%s install` as your normal user, not with sudo — it will prompt for sudo only when writing the systemd unit", serviceName)
	}

	u, err := user.Current()
	if err != nil {
		return fmt.Errorf("cannot determine current user: %w", err)
	}
	groupName := u.Gid
	if g, gerr := user.LookupGroupId(u.Gid); gerr == nil {
		groupName = g.Name
	}

	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot locate the running binary: %w", err)
	}
	if resolved, rerr := filepath.EvalSymlinks(exePath); rerr == nil {
		exePath = resolved
	}

	// 1. Choose target bin dir: ~/bin if it exists, else ~/.local/bin (created).
	binDir := filepath.Join(u.HomeDir, "bin")
	if !dirExists(binDir) {
		binDir = filepath.Join(u.HomeDir, ".local", "bin")
		if mkErr := os.MkdirAll(binDir, 0o755); mkErr != nil {
			return fmt.Errorf("creating %s: %w", binDir, mkErr)
		}
	}
	targetBin := filepath.Join(binDir, serviceName)

	// 2. Copy the binary into place (atomic rename avoids "text file busy" when
	// reinstalling over a running copy).
	if err := copyBinary(exePath, targetBin); err != nil {
		return fmt.Errorf("copying binary to %s: %w", targetBin, err)
	}
	fmt.Printf("Installed binary: %s\n", targetBin)

	// 2b. Symlink openclaw -> claw. rabbit-agent on the Rabbit R1 spawns
	// `openclaw acp`, so the binary has to be reachable under that name for the
	// R1 to connect. `make install` does this too; without it here, an install
	// from a release binary silently lacks the R1 path.
	if err := linkOpenClawAlias(binDir, serviceName); err != nil {
		// Not fatal: everything except the R1's ACP bridge works without it.
		fmt.Printf("Warning: could not create the openclaw alias (%v).\n"+
			"  The Rabbit R1 spawns `openclaw acp`; without this link it cannot connect.\n", err)
	} else {
		fmt.Printf("Installed alias:  %s -> %s (for the Rabbit R1's `openclaw acp`)\n",
			filepath.Join(binDir, openClawAlias), serviceName)
	}

	// 3. Ensure the bin dir is on PATH for interactive shells.
	if note := ensurePath(binDir); note != "" {
		fmt.Println(note)
	}

	// 3b. Apply requested bind host/port to the config before the service starts,
	// so a headless host is reachable on first boot without a manual config edit.
	if host != "" || port != 0 {
		if err := applyServerSettings(host, port); err != nil {
			return fmt.Errorf("applying server settings: %w", err)
		}
	}

	// 3c. Apply the IP allowlist. Binding off-box without one produces an install
	// that listens on the network and then refuses every connection from it, which
	// looks like a firewall problem rather than a configuration choice. Fail here,
	// where the operator is standing right next to it and can fix it in one flag,
	// rather than at 3am on a headless box.
	if allowedCIDRs == "" && isNetworkBind(host) {
		if existing, err := network.CurrentAllowlist(); err == nil && len(existing) == 0 {
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
	}

	// 4. Write the systemd unit to a temp file, then install it with one sudo call.
	unit := buildUnit(u.Username, groupName, targetBin, binDir)
	tmp, err := os.CreateTemp("", "claw-service-*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp unit file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, werr := tmp.WriteString(unit); werr != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing temp unit file: %w", werr)
	}
	_ = tmp.Close()

	fmt.Printf("Registering systemd service %q as user %s:%s (sudo password may be required)…\n",
		serviceName, u.Username, groupName)
	script := strings.Join([]string{
		fmt.Sprintf("cp %s %s", shellQuote(tmpPath), shellQuote(unitPath)),
		fmt.Sprintf("chmod 0644 %s", shellQuote(unitPath)),
		"systemctl daemon-reload",
		fmt.Sprintf("systemctl enable --now %s", shellQuote(serviceName)),
	}, " && ")
	if err := runSudo(script); err != nil {
		return fmt.Errorf("installing systemd service: %w", err)
	}

	fmt.Printf("\n%s is installed and running.\n", app.Name())
	fmt.Printf("  Open:   %s\n", accessURL())
	fmt.Printf("  Status: systemctl status %s\n", serviceName)
	fmt.Printf("  Logs:   journalctl -u %s -f   (or %s/logs/claw.log)\n", serviceName, dataDir(u.HomeDir))
	fmt.Printf("  Stop/remove: %s uninstall\n", serviceName)
	return nil
}

// accessURL returns the web UI URL to print after install, derived from the
// active bind host/port. For an all-interfaces bind it uses the host's primary
// private IP so a headless user gets a reachable address, not "0.0.0.0".
func accessURL() string {
	host, port := "127.0.0.1", config.DefaultGatewayPort
	if cfg, err := config.LoadConfig(internal.GetConfigPath()); err == nil {
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

func runUninstall() error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("`%s uninstall` is only supported on Linux (systemd); this is %s", serviceName, runtime.GOOS)
	}
	if os.Geteuid() == 0 {
		return fmt.Errorf("run `%s uninstall` as your normal user, not with sudo", serviceName)
	}

	fmt.Printf("Removing systemd service %q (sudo password may be required)…\n", serviceName)
	// `;` (not `&&`) so a missing/already-stopped service doesn't abort cleanup.
	script := strings.Join([]string{
		fmt.Sprintf("systemctl disable --now %s", shellQuote(serviceName)),
		fmt.Sprintf("rm -f %s", shellQuote(unitPath)),
		"systemctl daemon-reload",
	}, "; ")
	if err := runSudo(script); err != nil {
		return fmt.Errorf("removing systemd service: %w", err)
	}

	fmt.Printf("\nService removed. The installed binary and PATH entry were left in place.\n")
	return nil
}

// buildUnit renders the systemd system unit. The service runs as the invoking
// user/group so it has access to that user's ~/.claw data directory. CLAW_HOME is
// only set when a non-default data dir is in effect at install time.
func buildUnit(username, group, execPath, binDir string) string {
	var b strings.Builder
	b.WriteString("[Unit]\n")
	b.WriteString("Description=" + app.Name() + " — " + app.TagLine() + "\n")
	b.WriteString("After=network-online.target\n")
	b.WriteString("Wants=network-online.target\n\n")

	b.WriteString("[Service]\n")
	b.WriteString("Type=simple\n")
	b.WriteString("User=" + username + "\n")
	b.WriteString("Group=" + group + "\n")
	b.WriteString("ExecStart=" + execPath + "\n")
	b.WriteString("Restart=on-failure\n")
	b.WriteString("RestartSec=5\n")
	b.WriteString("Environment=PATH=" + servicePATH(binDir) + "\n")
	if home := os.Getenv(global.EnvVarHome); home != "" {
		b.WriteString("Environment=" + global.EnvVarHome + "=" + home + "\n")
	}
	b.WriteString("\n[Install]\n")
	b.WriteString("WantedBy=multi-user.target\n")
	return b.String()
}

// applyServerSettings writes the requested bind host/port into the config (a
// blank/zero value leaves the existing one untouched), creating the config from
// defaults if it doesn't exist yet. It warns when binding a non-loopback address
// because the WebUI has no authentication.
func applyServerSettings(host string, port int) error {
	path := internal.GetConfigPath()
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

// applyAllowlist writes a custom IP allowlist (comma-separated CIDRs) into
// gateway.allowed_cidrs in config.json. Each CIDR is validated before saving.
// openClawAlias is the name rabbit-agent spawns (`openclaw acp`). ClawEh serves
// ACP from the same binary, so the alias is a symlink rather than a second build.
const openClawAlias = "openclaw"

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

// servicePATH builds the PATH baked into the systemd unit: binDir first, then the
// user's current interactive PATH (captured at install time — this is what makes
// CLI agents in ~/.local/bin or an nvm node bin reachable by the service, for both
// detection and execution), with the standard system dirs appended as a backstop.
// Note: an nvm path is tied to the active node version; switch versions and you'll
// need to re-run `claw install` to refresh it.
func servicePATH(binDir string) string {
	parts := []string{binDir}
	seen := map[string]bool{binDir: true}
	add := func(p string) {
		if p != "" && !seen[p] {
			parts = append(parts, p)
			seen[p] = true
		}
	}
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		add(p)
	}
	for _, p := range []string{"/usr/local/bin", "/usr/bin", "/bin"} {
		add(p)
	}
	return strings.Join(parts, ":")
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
		return fmt.Sprintf("PATH already configured in %s (restart your shell if `%s` isn't found).", rc, serviceName)
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

func copyBinary(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return fileutil.WriteFileAtomic(dst, data, 0o755)
}

func runSudo(script string) error {
	cmd := exec.Command("sudo", "bash", "-c", script)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
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

// shellQuote single-quotes s for safe inclusion in the sudo bash script.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}
