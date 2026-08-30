//go:build windows

package mcp

import (
	"os/exec"
	"strconv"
)

func prepareStdioCommand(cmd *exec.Cmd) {
	// no-op on Windows
}

func terminateStdioProcessTree(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	if pid <= 0 {
		return
	}
	_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run()
}
