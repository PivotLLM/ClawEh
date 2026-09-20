package install

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/PivotLLM/ClawEh/global"
)

const (
	launchdLabel      = "com.pivotllm.claweh"
	launchdPlistName  = "com.pivotllm.claweh.plist"
	systemLaunchdPath = "/Library/LaunchDaemons/" + launchdPlistName
)

// userLaunchdPath returns the path to the LaunchAgent plist for the given user.
func userLaunchdPath(homeDir string) string {
	return filepath.Join(homeDir, "Library", "LaunchAgents", launchdPlistName)
}

// buildLaunchdPlist renders the launchd XML plist for macOS.
// If isSystem is true, UserName and GroupName are included to ensure ClawEh drops
// root privileges and runs as the target user.
func buildLaunchdPlist(label, username, groupname, execPath, homeDir, binDir, clawHome string, isSystem bool) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString(`<plist version="1.0">` + "\n")
	b.WriteString("<dict>\n")
	fmt.Fprintf(&b, "\t<key>Label</key>\n\t<string>%s</string>\n", label)

	if isSystem && username != "" {
		fmt.Fprintf(&b, "\t<key>UserName</key>\n\t<string>%s</string>\n", username)
		if groupname != "" {
			fmt.Fprintf(&b, "\t<key>GroupName</key>\n\t<string>%s</string>\n", groupname)
		}
	}

	if homeDir != "" {
		fmt.Fprintf(&b, "\t<key>WorkingDirectory</key>\n\t<string>%s</string>\n", homeDir)
	}

	b.WriteString("\t<key>ProgramArguments</key>\n\t<array>\n")
	fmt.Fprintf(&b, "\t\t<string>%s</string>\n", execPath)
	b.WriteString("\t</array>\n")

	b.WriteString("\t<key>RunAtLoad</key>\n\t<true/>\n")
	b.WriteString("\t<key>KeepAlive</key>\n\t<true/>\n")

	// Environment variables
	b.WriteString("\t<key>EnvironmentVariables</key>\n\t<dict>\n")
	if homeDir != "" {
		fmt.Fprintf(&b, "\t\t<key>HOME</key>\n\t\t<string>%s</string>\n", homeDir)
	}
	fmt.Fprintf(&b, "\t\t<key>PATH</key>\n\t\t<string>%s</string>\n", servicePATH(homeDir, binDir))
	if clawHome != "" {
		fmt.Fprintf(&b, "\t\t<key>%s</key>\n\t\t<string>%s</string>\n", global.EnvVarHome, clawHome)
	}
	b.WriteString("\t</dict>\n")

	// Standard log paths
	logFile := "/tmp/claw-launchd.log"
	if clawHome != "" {
		logFile = filepath.Join(clawHome, "logs", "claw-launchd.log")
	} else if homeDir != "" {
		logFile = filepath.Join(homeDir, ".claw", "logs", "claw-launchd.log")
	}
	fmt.Fprintf(&b, "\t<key>StandardOutPath</key>\n\t<string>%s</string>\n", logFile)
	fmt.Fprintf(&b, "\t<key>StandardErrorPath</key>\n\t<string>%s</string>\n", logFile)

	b.WriteString("</dict>\n</plist>\n")
	return b.String()
}

// installLaunchd writes the launchd plist and loads the service via launchctl.
func installLaunchd(tu *TargetUser, targetBin, binDir, clawHome string) error {
	// Ensure user log directory exists
	logsDir := filepath.Join(clawHome, "logs")
	_ = os.MkdirAll(logsDir, 0o755)

	if tu.IsRoot {
		// System Mode: writes to /Library/LaunchDaemons/com.pivotllm.claweh.plist
		plist := buildLaunchdPlist(launchdLabel, tu.Username, tu.GroupName, targetBin, tu.HomeDir, binDir, clawHome, true)
		if err := os.WriteFile(systemLaunchdPath, []byte(plist), 0o644); err != nil {
			return fmt.Errorf("writing launchd daemon %s: %w", systemLaunchdPath, err)
		}
		fmt.Printf("Registered launchd daemon: %s (running as %s:%s)\n", systemLaunchdPath, tu.Username, tu.GroupName)

		// Unload previous instance if present
		_ = exec.Command("launchctl", "bootout", "system/"+launchdLabel).Run()
		_ = exec.Command("launchctl", "unload", "-w", systemLaunchdPath).Run()

		// Bootstrap service (modern launchctl fallback to load -w)
		cmdBootstrap := exec.Command("launchctl", "bootstrap", "system", systemLaunchdPath)
		if out, err := cmdBootstrap.CombinedOutput(); err != nil {
			// Fallback to legacy launchctl load
			cmdLoad := exec.Command("launchctl", "load", "-w", systemLaunchdPath)
			if loadOut, loadErr := cmdLoad.CombinedOutput(); loadErr != nil {
				return fmt.Errorf("launchctl bootstrap system: %s / %s (%w)", string(out), string(loadOut), loadErr)
			}
		}
		fmt.Printf("Loaded launchd system daemon %s\n", launchdLabel)
	} else {
		// User Mode: writes to ~/Library/LaunchAgents/com.pivotllm.claweh.plist
		plist := buildLaunchdPlist(launchdLabel, tu.Username, tu.GroupName, targetBin, tu.HomeDir, binDir, clawHome, false)
		destPath := userLaunchdPath(tu.HomeDir)
		if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
			return fmt.Errorf("creating directory %s: %w", filepath.Dir(destPath), err)
		}
		if err := os.WriteFile(destPath, []byte(plist), 0o644); err != nil {
			return fmt.Errorf("writing launchd agent %s: %w", destPath, err)
		}
		fmt.Printf("Registered launchd user agent: %s\n", destPath)

		// Unload previous instance if present
		guiTarget := "gui/" + tu.UID
		_ = exec.Command("launchctl", "bootout", guiTarget+"/"+launchdLabel).Run()
		_ = exec.Command("launchctl", "unload", "-w", destPath).Run()

		// Bootstrap service (modern launchctl fallback to load -w)
		cmdBootstrap := exec.Command("launchctl", "bootstrap", guiTarget, destPath)
		if out, err := cmdBootstrap.CombinedOutput(); err != nil {
			// Fallback to legacy launchctl load
			cmdLoad := exec.Command("launchctl", "load", "-w", destPath)
			if loadOut, loadErr := cmdLoad.CombinedOutput(); loadErr != nil {
				return fmt.Errorf("launchctl bootstrap %s: %s / %s (%w)", guiTarget, string(out), string(loadOut), loadErr)
			}
		}
		fmt.Printf("Loaded launchd user agent %s\n", launchdLabel)
	}
	return nil
}

// uninstallLaunchd unloads and removes the launchd service plist.
func uninstallLaunchd(tu *TargetUser) error {
	var errs []string
	if tu.IsRoot {
		_ = exec.Command("launchctl", "bootout", "system/"+launchdLabel).Run()
		_ = exec.Command("launchctl", "unload", "-w", systemLaunchdPath).Run()
		if err := os.Remove(systemLaunchdPath); err != nil && !os.IsNotExist(err) {
			errs = append(errs, fmt.Sprintf("removing %s: %v", systemLaunchdPath, err))
		}
		fmt.Printf("Removed launchd daemon: %s\n", systemLaunchdPath)
	} else {
		guiTarget := "gui/" + tu.UID
		destPath := userLaunchdPath(tu.HomeDir)
		_ = exec.Command("launchctl", "bootout", guiTarget+"/"+launchdLabel).Run()
		_ = exec.Command("launchctl", "unload", "-w", destPath).Run()
		if err := os.Remove(destPath); err != nil && !os.IsNotExist(err) {
			errs = append(errs, fmt.Sprintf("removing %s: %v", destPath, err))
		}
		fmt.Printf("Removed launchd agent: %s\n", destPath)
	}

	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// isLaunchdServiceActive checks if launchd is managing the service.
func isLaunchdServiceActive(label string) bool {
	cmd := exec.Command("launchctl", "list")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return bytes.Contains(out, []byte(label))
}
