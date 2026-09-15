package install

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/PivotLLM/ClawEh/app"
	"github.com/PivotLLM/ClawEh/global"
)

const (
	systemUnitPath = "/etc/systemd/system/claw.service"
	unitFileName   = "claw.service"
)

// buildSystemUnit renders the systemd system unit for running at machine boot.
// It explicitly sets User, Group, WorkingDirectory, and environment variables so that
// ClawEh executes strictly as the target user with access to that user's ~/.claw.
func buildSystemUnit(username, group, execPath, homeDir, binDir string) string {
	var b strings.Builder
	b.WriteString("[Unit]\n")
	b.WriteString("Description=" + app.Name() + " — " + app.TagLine() + "\n")
	b.WriteString("After=network-online.target\n")
	b.WriteString("Wants=network-online.target\n\n")

	b.WriteString("[Service]\n")
	b.WriteString("Type=simple\n")
	b.WriteString("User=" + username + "\n")
	b.WriteString("Group=" + group + "\n")
	if homeDir != "" {
		b.WriteString("WorkingDirectory=" + homeDir + "\n")
	}
	b.WriteString("ExecStart=" + execPath + "\n")
	b.WriteString("Restart=on-failure\n")
	b.WriteString("RestartSec=5\n")
	b.WriteString("KillMode=control-group\n")
	b.WriteString("TimeoutStopSec=30\n")
	if homeDir != "" {
		b.WriteString("Environment=HOME=" + homeDir + "\n")
	}
	b.WriteString("Environment=PATH=" + servicePATH(binDir) + "\n")
	if home := os.Getenv(global.EnvVarHome); home != "" {
		b.WriteString("Environment=" + global.EnvVarHome + "=" + home + "\n")
	}
	b.WriteString("\n[Install]\n")
	b.WriteString("WantedBy=multi-user.target\n")
	return b.String()
}

// buildUserUnit renders the systemd user unit for running in user space (~/.config/systemd/user/).
// In user units, User= and Group= directives are not permitted by systemd.
func buildUserUnit(execPath, homeDir, binDir string) string {
	var b strings.Builder
	b.WriteString("[Unit]\n")
	b.WriteString("Description=" + app.Name() + " — " + app.TagLine() + "\n")
	b.WriteString("After=network-online.target\n")
	b.WriteString("Wants=network-online.target\n\n")

	b.WriteString("[Service]\n")
	b.WriteString("Type=simple\n")
	if homeDir != "" {
		b.WriteString("WorkingDirectory=" + homeDir + "\n")
	}
	b.WriteString("ExecStart=" + execPath + "\n")
	b.WriteString("Restart=on-failure\n")
	b.WriteString("RestartSec=5\n")
	b.WriteString("KillMode=control-group\n")
	b.WriteString("TimeoutStopSec=30\n")
	if homeDir != "" {
		b.WriteString("Environment=HOME=" + homeDir + "\n")
	}
	b.WriteString("Environment=PATH=" + servicePATH(binDir) + "\n")
	if home := os.Getenv(global.EnvVarHome); home != "" {
		b.WriteString("Environment=" + global.EnvVarHome + "=" + home + "\n")
	}
	b.WriteString("\n[Install]\n")
	b.WriteString("WantedBy=default.target\n")
	return b.String()
}

// userUnitPath returns the path to the systemd user unit for the given user.
func userUnitPath(homeDir string) string {
	return filepath.Join(homeDir, ".config", "systemd", "user", unitFileName)
}

// installSystemd writes the systemd unit file and enables/starts the service.
func installSystemd(tu *TargetUser, targetBin, binDir string) error {
	if tu.IsRoot {
		// System Mode: writes to /etc/systemd/system/claw.service
		unit := buildSystemUnit(tu.Username, tu.GroupName, targetBin, tu.HomeDir, binDir)
		if err := os.WriteFile(systemUnitPath, []byte(unit), 0o644); err != nil {
			return fmt.Errorf("writing systemd unit %s: %w", systemUnitPath, err)
		}
		fmt.Printf("Registered systemd system service: %s (running as %s:%s)\n", systemUnitPath, tu.Username, tu.GroupName)

		cmdReload := exec.Command("systemctl", "daemon-reload")
		if out, err := cmdReload.CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl daemon-reload: %s (%w)", string(out), err)
		}

		cmdEnable := exec.Command("systemctl", "enable", "--now", serviceName)
		if out, err := cmdEnable.CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl enable --now %s: %s (%w)", serviceName, string(out), err)
		}
	} else {
		// User Mode: writes to ~/.config/systemd/user/claw.service
		unit := buildUserUnit(targetBin, tu.HomeDir, binDir)
		destPath := userUnitPath(tu.HomeDir)
		if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
			return fmt.Errorf("creating directory %s: %w", filepath.Dir(destPath), err)
		}
		if err := os.WriteFile(destPath, []byte(unit), 0o644); err != nil {
			return fmt.Errorf("writing user systemd unit %s: %w", destPath, err)
		}
		fmt.Printf("Registered systemd user service: %s\n", destPath)

		cmdReload := exec.Command("systemctl", "--user", "daemon-reload")
		if out, err := cmdReload.CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl --user daemon-reload: %s (%w)", string(out), err)
		}

		cmdEnable := exec.Command("systemctl", "--user", "enable", "--now", serviceName)
		if out, err := cmdEnable.CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl --user enable --now %s: %s (%w)", serviceName, string(out), err)
		}

		// Try enabling lingering so user service continues running without active session
		cmdLinger := exec.Command("loginctl", "enable-linger", tu.Username)
		if err := cmdLinger.Run(); err == nil {
			fmt.Printf("Enabled systemd user lingering for %s (service will start at boot).\n", tu.Username)
		} else {
			fmt.Printf("Note: To allow user service to start at boot without login, run: loginctl enable-linger %s\n", tu.Username)
		}
	}
	return nil
}

// uninstallSystemd stops, disables, and removes the systemd unit.
func uninstallSystemd(tu *TargetUser) error {
	var errs []string

	if tu.IsRoot {
		// Stop and disable system service
		_ = exec.Command("systemctl", "disable", "--now", serviceName).Run()
		if err := os.Remove(systemUnitPath); err != nil && !os.IsNotExist(err) {
			errs = append(errs, fmt.Sprintf("removing %s: %v", systemUnitPath, err))
		}
		_ = exec.Command("systemctl", "daemon-reload").Run()
		fmt.Printf("Removed systemd system service: %s\n", systemUnitPath)
	} else {
		// Stop and disable user service
		_ = exec.Command("systemctl", "--user", "disable", "--now", serviceName).Run()
		destPath := userUnitPath(tu.HomeDir)
		if err := os.Remove(destPath); err != nil && !os.IsNotExist(err) {
			errs = append(errs, fmt.Sprintf("removing %s: %v", destPath, err))
		}
		_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
		fmt.Printf("Removed systemd user service: %s\n", destPath)
	}

	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// isSystemdServiceActive checks if either user or system service is active.
func isSystemdServiceActive(homeDir string) (userActive bool, systemActive bool) {
	if err := exec.Command("systemctl", "--user", "is-active", "--quiet", serviceName).Run(); err == nil {
		userActive = true
	}
	if err := exec.Command("systemctl", "is-active", "--quiet", serviceName).Run(); err == nil {
		systemActive = true
	}
	return userActive, systemActive
}

// restartSystemd attempts to restart the running systemd service.
func restartSystemd() error {
	userActive, systemActive := isSystemdServiceActive("")
	if userActive {
		fmt.Printf("Restarting systemd user service %s...\n", serviceName)
		cmd := exec.Command("systemctl", "--user", "restart", serviceName)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl --user restart %s: %s (%w)", serviceName, string(out), err)
		}
		fmt.Println("Service restarted successfully.")
		return nil
	}
	if systemActive {
		if os.Geteuid() == 0 {
			fmt.Printf("Restarting systemd system service %s...\n", serviceName)
			cmd := exec.Command("systemctl", "restart", serviceName)
			if out, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("systemctl restart %s: %s (%w)", serviceName, string(out), err)
			}
			fmt.Println("Service restarted successfully.")
			return nil
		}
		fmt.Printf("Note: System service %s is running. Restart it with: sudo systemctl restart %s\n", serviceName, serviceName)
	}
	return nil
}
