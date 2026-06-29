// Package install provides the `claw install` / `claw uninstall` subcommands,
// which deploy the running binary and register a systemd system service that
// runs ClawEh as the invoking user at boot.
package install

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/PivotLLM/ClawEh/pkg/fileutil"
	"github.com/PivotLLM/ClawEh/pkg/global"
)

const (
	serviceName = "claw"
	unitPath    = "/etc/systemd/system/claw.service"
)

// NewInstallCommand returns the `claw install` subcommand.
func NewInstallCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Install the binary and register a systemd service that starts " + global.AppName + " at boot",
		Long: "Copies the running binary to ~/bin (or ~/.local/bin), ensures that directory is on\n" +
			"your PATH, and writes a systemd system service that runs " + global.AppName + " as your user\n" +
			"account at boot. Writing the service unit requires sudo; you'll be prompted for your\n" +
			"password. Run this as your normal user, not with sudo.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runInstall()
		},
	}
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

func runInstall() error {
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

	// 3. Ensure the bin dir is on PATH for interactive shells.
	if note := ensurePath(binDir); note != "" {
		fmt.Println(note)
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

	fmt.Printf("\n%s is installed and running.\n", global.AppName)
	fmt.Printf("  Status: systemctl status %s\n", serviceName)
	fmt.Printf("  Logs:   journalctl -u %s -f   (or %s/logs/claw.log)\n", serviceName, dataDir(u.HomeDir))
	fmt.Printf("  Stop/remove: %s uninstall\n", serviceName)
	return nil
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
	b.WriteString("Description=" + global.AppName + " — " + global.AppTagLine + "\n")
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
