//go:build !windows

package mcp

import (
	"os/exec"
	"syscall"
)

// prepareStdioCommand makes the child its own process-group leader so the whole
// tree (e.g. npx -> node -> chromium) can be signalled together. Without it,
// killing only the direct child orphans grandchildren that keep resources locked
// — playwright's persistent --user-data-dir profile lock is the motivating case.
func prepareStdioCommand(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// terminateStdioProcessTree kills the child's entire process group. Safe to call
// after the SDK has already reaped the direct child: the group signal mops up any
// surviving grandchildren, and a stale/reused pid just yields a no-op ESRCH.
func terminateStdioProcessTree(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	if pid <= 0 {
		return
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}
