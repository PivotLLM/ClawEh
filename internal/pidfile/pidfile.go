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
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/PivotLLM/ClawEh/fileutil"
	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/internal/perms"
	"github.com/PivotLLM/ClawEh/logger"
)

// Name is the file written into the data directory's internal/.
const Name = "claw.pid"

// Path returns the PID file path for a data directory.
func Path(dataDir string) string { return filepath.Join(dataDir, global.InternalDir, Name) }

// Write records the current process id. A failure is returned rather than
// fatal: the PID file is a convenience for reporting, and an instance that
// cannot write one should still start.
func Write(dataDir string) error {
	if dataDir == "" {
		return errors.New("pidfile: no data directory")
	}
	p := Path(dataDir)
	if err := os.MkdirAll(filepath.Dir(p), perms.SecretModes().Dir); err != nil {
		return fmt.Errorf("pidfile: create %s: %w", filepath.Dir(p), err)
	}
	if err := fileutil.WriteFileAtomic(p, []byte(strconv.Itoa(os.Getpid())+"\n"), perms.SecretModes().File); err != nil {
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
	p := Path(dataDir)
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		logger.WarnCF("gateway", "failed to remove PID file", map[string]any{"path": p, "error": err.Error()})
	}
}

// Read returns the pid recorded for a data directory and whether that process
// is a LIVE ClawEh process.
//
// Both checks matter. A kill -9 leaves the file behind, and pids are recycled,
// so a file alone proves nothing: the process must still exist AND still run
// this program. Without the second check a status command would cheerfully report the
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

// isClaw reports whether pid is alive and is a ClawEh process.
//
// Signal 0 tests for existence without delivering anything; it succeeds only
// for a process this user may signal. isOurExecutable then rejects a recycled
// pid now belonging to something else.
func isClaw(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if sigErr := proc.Signal(syscall.Signal(0)); sigErr != nil {
		return false
	}
	return isOurExecutable(pid)
}

// procRoot and selfExecutable are replaced in tests.
var (
	procRoot       = "/proc"
	selfExecutable = os.Executable
)

// deletedSuffix is what Linux appends to /proc/<pid>/exe once the file has
// been replaced, as `upgrade` does to a running binary.
const deletedSuffix = " (deleted)"

// isOurExecutable reports whether pid runs this program, judged by its
// executable rather than the name it was started as, so a gateway started
// through the openclaw link or under another name is still recognised.
//
// /proc/<pid>/exe is the executable with every link resolved. It matches when
// it is the file this process runs, or a file with the installed binary's
// name (another build of it, such as the installed one seen from a
// development copy). Where exe cannot be read (another user's process), the
// command name is compared instead. On a system without /proc (macOS),
// existence is the best available answer.
func isOurExecutable(pid int) bool {
	dir := filepath.Join(procRoot, strconv.Itoa(pid))
	self := ""
	if p, err := selfExecutable(); err == nil {
		self = strings.TrimSuffix(p, deletedSuffix)
		if resolved, err := filepath.EvalSymlinks(self); err == nil {
			self = resolved
		}
	}
	if exe, err := os.Readlink(filepath.Join(dir, "exe")); err == nil {
		exe = strings.TrimSuffix(exe, deletedSuffix)
		return exe == self || filepath.Base(exe) == global.InstalledBinaryName
	}
	comm, err := os.ReadFile(filepath.Join(dir, "comm")) //nolint:gosec // a /proc entry named by a pid
	if err != nil {
		return true // no procfs: existence is the best available answer
	}
	name := strings.TrimSpace(string(comm))
	return name == global.InstalledBinaryName || (self != "" && name == commName(filepath.Base(self)))
}

// commName is name as /proc/<pid>/comm shows it: at most 15 bytes.
func commName(name string) string {
	if len(name) > 15 {
		return name[:15]
	}
	return name
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
	for line := range strings.SplitSeq(string(b), "\n") {
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
