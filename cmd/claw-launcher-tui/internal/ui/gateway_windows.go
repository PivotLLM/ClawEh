//go:build windows
// +build windows

package ui

import (
	"os"
	"os/exec"
	"path/filepath"
)

func isGatewayProcessRunning() bool {
	binaryName := filepath.Base(os.Args[0])
	cmd := exec.Command("tasklist", "/FI", "IMAGENAME eq "+binaryName+".exe")
	return cmd.Run() == nil
}

func stopGatewayProcess() error {
	binaryName := filepath.Base(os.Args[0])
	cmd := exec.Command("taskkill", "/F", "/IM", binaryName+".exe")
	return cmd.Run()
}
