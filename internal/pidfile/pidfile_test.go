// ClawEh - gateway PID file
// License: MIT

package pidfile

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestWriteReadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if err := Write(dir); err != nil {
		t.Fatalf("Write: %v", err)
	}
	b, err := os.ReadFile(Path(dir))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got := string(b); got != strconv.Itoa(os.Getpid())+"\n" {
		t.Errorf("file contains %q, want this process's pid", got)
	}

	// The test binary IS the running process, but it is not named "claw", so
	// the comm check correctly refuses to call it a gateway. That the pid is
	// still returned is what lets a caller report a mismatch if it wants to.
	pid, _ := Read(dir)
	if pid != os.Getpid() {
		t.Errorf("Read pid = %d, want %d", pid, os.Getpid())
	}
}

// A pid file left behind by a kill -9 must not read as a running gateway.
// Without this, `claw status` would report the memory of whatever unrelated
// process later inherited the number.
func TestReadRejectsADeadProcess(t *testing.T) {
	dir := t.TempDir()
	// A pid that cannot be live: the kernel's maximum plus one.
	if err := os.WriteFile(Path(dir), []byte("4194305\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, running := Read(dir); running {
		t.Error("a dead pid reported as running")
	}
}

func TestReadHandlesMissingAndJunk(t *testing.T) {
	dir := t.TempDir()
	if pid, running := Read(dir); pid != 0 || running {
		t.Errorf("missing file: pid=%d running=%v, want 0/false", pid, running)
	}
	for _, junk := range []string{"", "  ", "not-a-number", "-1", "0"} {
		if err := os.WriteFile(Path(dir), []byte(junk), 0o644); err != nil {
			t.Fatalf("write %q: %v", junk, err)
		}
		if pid, running := Read(dir); pid != 0 || running {
			t.Errorf("junk %q: pid=%d running=%v, want 0/false", junk, pid, running)
		}
	}
	if pid, running := Read(""); pid != 0 || running {
		t.Errorf("empty dataDir: pid=%d running=%v, want 0/false", pid, running)
	}
}

func TestRemove(t *testing.T) {
	dir := t.TempDir()
	if err := Write(dir); err != nil {
		t.Fatalf("Write: %v", err)
	}
	Remove(dir)
	if _, err := os.Stat(Path(dir)); !os.IsNotExist(err) {
		t.Errorf("file still present after Remove: %v", err)
	}
	Remove(dir) // already gone: not an error
	Remove("")  // no data dir: not a panic
}

func TestWriteCreatesTheDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does", "not", "exist")
	if err := Write(dir); err != nil {
		t.Fatalf("Write into a missing directory: %v", err)
	}
	if _, err := os.Stat(Path(dir)); err != nil {
		t.Errorf("pid file not created: %v", err)
	}
}

// RSS is read from VmRSS, which is the same figure ps reports — not VmSize,
// which for a Go process counts over a gigabyte of reserved address space.
func TestRSSBytesIsResidentNotVirtual(t *testing.T) {
	rss, ok := RSSBytes(os.Getpid())
	if !ok {
		t.Skip("no /proc on this platform")
	}
	if rss <= 0 {
		t.Fatalf("RSS = %d, want a positive size", rss)
	}
	// A Go test binary is megabytes resident, never gigabytes. A gigabyte-scale
	// answer would mean VmSize had been read by mistake.
	if rss > 2<<30 {
		t.Errorf("RSS = %d bytes (>2 GB) — that looks like VmSize, not VmRSS", rss)
	}
	if _, ok := RSSBytes(4194305); ok {
		t.Error("RSSBytes reported a size for a pid that cannot exist")
	}
}
