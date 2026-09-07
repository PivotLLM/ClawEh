// ClawEh - process PID file
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

// Package pidfile records the running ClawEh process id in its data
// directory, so a separate `claw` invocation can find the instance it belongs
// to and report on it.
//
// The data directory is the right scope. One binary runs several instances —
// a production service and a developer one on the same host — and a CLI command
// already resolves CLAW_HOME to find the config. Looking the process up through
// systemd instead would need the command to know which unit it is, which it has
// no way to determine.
package pidfile

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Name is the file written into the data directory.
const Name = "claw.pid"

// Path returns the PID file path for a data directory.
func Path(dataDir string) string { return filepath.Join(dataDir, Name) }

// Write records the current process id. A failure is returned rather than
// fatal: the PID file is a convenience for reporting, and an instance that
// cannot write one should still start.
func Write(dataDir string) error {
	if dataDir == "" {
		return fmt.Errorf("pidfile: no data directory")
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("pidfile: create %s: %w", dataDir, err)
	}
	p := Path(dataDir)
	if err := os.WriteFile(p, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644); err != nil {
		return fmt.Errorf("pidfile: write %s: %w", p, err)
	}
	return nil
}

// Remove deletes the PID file. Missing is not an error — the point is that it
// is gone.
func Remove(dataDir string) {
	if dataDir == "" {
		return
	}
	_ = os.Remove(Path(dataDir))
}

// Read returns the pid recorded for a data directory and whether that process
// is a LIVE ClawEh process.
//
// Both checks matter. A kill -9 leaves the file behind, and pids are recycled,
// so a file alone proves nothing: the process must still exist AND still be
// claw. Without the second check a status command would cheerfully report the
// memory of whatever unrelated program inherited the number.
func Read(dataDir string) (pid int, running bool) {
	if dataDir == "" {
		return 0, false
	}
	b, err := os.ReadFile(Path(dataDir))
	if err != nil {
		return 0, false
	}
	pid, err = strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, isClaw(pid)
}

// isClaw reports whether pid is alive and is a claw process.
//
// Signal 0 tests for existence without delivering anything; it succeeds only
// for a process this user may signal. The comm check then rejects a recycled
// pid now belonging to something else. On a system without /proc, existence
// alone has to do.
func isClaw(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		return false
	}
	comm, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	if err != nil {
		return true // no procfs: existence is the best available answer
	}
	return strings.TrimSpace(string(comm)) == "claw"
}

// RSSBytes returns a process's resident set size — the physical RAM it
// occupies — from /proc/<pid>/status.
//
// VmRSS is the same figure ps reports as RSS; they are not different metrics.
// Deliberately NOT VmSize, which for a Go process includes over a gigabyte of
// reserved address space and would suggest ClawEh is enormous when it is not.
func RSSBytes(pid int) (int64, bool) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, "VmRSS:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0, false
		}
		kb, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return 0, false
		}
		return kb * 1024, true
	}
	return 0, false
}
