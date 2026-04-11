//go:build !windows
// +build !windows

package ui

import (
	"os"
	"os/exec"
	"path/filepath"
)

func isGatewayProcessRunning() bool {
	binaryName := filepath.Base(os.Args[0])
	cmd := exec.Command("sh", "-c", "pgrep -f '"+binaryName+"\\s+gateway' >/dev/null 2>&1")
	return cmd.Run() == nil
}

func stopGatewayProcess() error {
	binaryName := filepath.Base(os.Args[0])
	cmd := exec.Command("sh", "-c", "pkill -f '"+binaryName+"\\s+gateway' >/dev/null 2>&1")
	return cmd.Run()
}
