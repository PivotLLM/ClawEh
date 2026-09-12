// ClawEh
// License: MIT

package status

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/app"
)

// captureStdout runs fn with os.Stdout redirected and returns what it printed.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = saved }()

	fn()
	_ = w.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// `claw status` reads configuration from disk and used to describe an
// installation rather than a running system — it could not say whether ClawEh
// was up. The process line is the answer, so its two states are worth pinning.
func TestPrintProcess_ReportsNotRunningWithoutAPidFile(t *testing.T) {
	got := captureStdout(t, func() { printProcess(t.TempDir()) })

	if !strings.Contains(got, "not running") {
		t.Errorf("output = %q, want it to say not running", got)
	}
	if !strings.Contains(got, app.Name()) {
		t.Errorf("output = %q, want it labelled with %q", got, app.Name())
	}
}

// A stale pid file left by a hard kill must read as "not running". Pids are
// recycled, so reporting the memory of whatever now holds that number would be
// worse than saying nothing.
func TestPrintProcess_TreatsAStalePidFileAsNotRunning(t *testing.T) {
	dir := t.TempDir()
	// Pid 1 exists on every Linux host and is emphatically not claw, which is
	// the case the comm check exists to reject.
	if err := os.WriteFile(filepath.Join(dir, "claw.pid"), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := captureStdout(t, func() { printProcess(dir) })

	if !strings.Contains(got, "not running") {
		t.Errorf("output = %q, want not running for a pid that is not claw", got)
	}
	if strings.Contains(got, "RAM") {
		t.Errorf("output = %q, want no memory figure for a process that is not ours", got)
	}
}

// The figure people quote is "my claw uses N MB", so the units are the whole
// point of this line. A regression to raw bytes, or to the wrong divisor, is
// invisible unless something asserts the rendering.
func TestHumanBytes(t *testing.T) {
	tests := []struct {
		name string
		in   int64
		want string
	}{
		{"zero", 0, "0 B"},
		{"bytes stay bytes", 512, "512 B"},
		{"one below the first boundary", 1023, "1023 B"},
		{"exactly one kilobyte", 1024, "1.0 KB"},
		{"kilobytes", 1536, "1.5 KB"},
		{"megabytes — the unit RSS is usually quoted in", 45 * 1024 * 1024, "45.0 MB"},
		{"a real reading", 42_356_736, "40.4 MB"},
		{"gigabytes", 3 * 1024 * 1024 * 1024, "3.0 GB"},
		// Binary units throughout: 1024, not 1000. Mixing the two is the classic
		// way this drifts.
		{"not decimal units", 1_000_000, "976.6 KB"},
		// The loop caps at terabytes rather than running off the end of "KMGT".
		{"terabytes", 5 * 1024 * 1024 * 1024 * 1024, "5.0 TB"},
		{"beyond the largest unit stays in it", 4096 * 1024 * 1024 * 1024 * 1024, "4096.0 TB"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := humanBytes(tc.in); got != tc.want {
				t.Errorf("humanBytes(%d) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
