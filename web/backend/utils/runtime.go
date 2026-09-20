package utils

import (
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// GetClawHome returns the claw home directory.
// Priority: $CLAW_HOME > ~/.claw
func GetClawHome() string {
	if home := os.Getenv("CLAW_HOME"); home != "" {
		return home
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claw")
}

// GetDefaultConfigPath returns the default path to the claw config file.
func GetDefaultConfigPath() string {
	if clawHome := os.Getenv("CLAW_HOME"); clawHome != "" {
		return filepath.Join(clawHome, "config.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "config.json"
	}
	return filepath.Join(home, ".claw", "config.json")
}

// FindClawBinary locates the claw executable.
// Search order:
//  1. CLAW_BINARY environment variable (explicit override)
//  2. Same directory as the current executable
//  3. Falls back to "claw" and relies on $PATH
func FindClawBinary() string {
	binaryName := "claw"
	if runtime.GOOS == "windows" {
		binaryName = "claw.exe"
	}

	if p := os.Getenv("CLAW_BINARY"); p != "" {
		if info, _ := os.Stat(p); info != nil && !info.IsDir() { //nolint:gosec // CLAW_BINARY is the operator's own environment
			return p
		}
	}

	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), binaryName)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}

	return "claw"
}

// GetLocalIP returns the local IP address of the machine.
func GetLocalIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok && !ipnet.IP.IsLoopback() && ipnet.IP.To4() != nil {
			return ipnet.IP.String()
		}
	}
	return ""
}

// OpenBrowser automatically opens the given URL in the default browser.
func OpenBrowser(url string) error {
	switch runtime.GOOS {
	case "linux":
		return exec.Command("xdg-open", url).Start() //nolint:gosec // fixed platform opener binary; url is a single argv entry, not a shell string
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start() //nolint:gosec // fixed platform opener binary; url is a single argv entry, not a shell string
	case "darwin":
		return exec.Command("open", url).Start() //nolint:gosec // fixed platform opener binary; url is a single argv entry, not a shell string
	default:
		return errors.New("unsupported platform")
	}
}
