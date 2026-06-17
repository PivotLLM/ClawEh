package logger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRollLogFile(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "claw.log")

	if err := EnableFileLogging(logPath, false); err != nil {
		t.Fatalf("EnableFileLogging: %v", err)
	}
	defer DisableFileLogging()
	SetLevel(DEBUG)
	SetErrorLogLevel(WARN)

	InfoCF("test", "an info line", nil)
	WarnCF("test", "a warning line", nil)

	// Force a known mtime so the archive name is deterministic.
	ts := time.Date(2026, 1, 2, 10, 0, 0, 0, time.Local)
	_ = os.Chtimes(logPath, ts, ts)
	_ = os.Chtimes(filepath.Join(dir, "error.log"), ts, ts)

	if err := RollLogFile(); err != nil {
		t.Fatalf("RollLogFile: %v", err)
	}

	// Both active logs are archived under the mtime date.
	clawArchive := filepath.Join(dir, "20260102-claw.log")
	errArchive := filepath.Join(dir, "20260102-error.log")
	if _, err := os.Stat(clawArchive); err != nil {
		t.Fatalf("expected dated claw archive: %v", err)
	}
	if _, err := os.Stat(errArchive); err != nil {
		t.Fatalf("expected dated error archive: %v", err)
	}

	// The warning is in both claw and error archives; the info only in claw.
	clawData, _ := os.ReadFile(clawArchive)
	errData, _ := os.ReadFile(errArchive)
	if !strings.Contains(string(clawData), "an info line") {
		t.Fatalf("claw archive missing info line: %q", clawData)
	}
	if !strings.Contains(string(errData), "a warning line") {
		t.Fatalf("error archive missing warning line: %q", errData)
	}
	if strings.Contains(string(errData), "an info line") {
		t.Fatalf("error.log should not contain info-level lines: %q", errData)
	}

	// Fresh active files are reopened and receive new writes.
	WarnCF("test", "after the roll", nil)
	freshErr, _ := os.ReadFile(filepath.Join(dir, "error.log"))
	if !strings.Contains(string(freshErr), "after the roll") {
		t.Fatalf("reopened error.log missing post-roll line: %q", freshErr)
	}
	if strings.Contains(string(freshErr), "a warning line") {
		t.Fatalf("reopened error.log should not retain pre-roll content: %q", freshErr)
	}
}

func TestRollLogFile_NoFileLogging(t *testing.T) {
	DisableFileLogging()
	if err := RollLogFile(); err != nil {
		t.Fatalf("RollLogFile with no file logging should be a no-op, got %v", err)
	}
}

func TestFatalWritesToAllSinksBeforeExit(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "claw.log")
	if err := EnableFileLogging(logPath, false); err != nil {
		t.Fatalf("EnableFileLogging: %v", err)
	}
	defer DisableFileLogging()
	SetLevel(DEBUG)
	SetErrorLogLevel(WARN)

	// Stub the exit so the process survives and we can inspect the sinks.
	var exitCode = -1
	prev := osExit
	osExit = func(code int) { exitCode = code }
	defer func() { osExit = prev }()

	FatalCF("test", "fatal boom", nil)

	if exitCode != 1 {
		t.Fatalf("expected exit code 1, got %d", exitCode)
	}
	clawData, _ := os.ReadFile(logPath)
	errData, _ := os.ReadFile(filepath.Join(dir, "error.log"))
	if !strings.Contains(string(clawData), "fatal boom") {
		t.Fatalf("claw.log missing fatal line: %q", clawData)
	}
	if !strings.Contains(string(errData), "fatal boom") {
		t.Fatalf("error.log missing fatal line: %q", errData)
	}
}
