// ClawEh
// License: MIT

package cronmsg

import (
	"testing"
	"time"
)

// TestBuildMarked pins the exact wrapper produced for a job with a fingerprint.
func TestBuildMarked(t *testing.T) {
	fireTime := time.Date(2026, 5, 31, 9, 40, 0, 0, time.UTC)
	out := Build("3f9a1c0d", fireTime, "self-check complete")
	want := "[3f9a1c0d] " + prefix + "2026-05-31 09:40 UTC:\n\nself-check complete"
	if out != want {
		t.Errorf("Build = %q, want %q", out, want)
	}
}

// TestBuildTimestampFormat pins the exact timestamp layout in the produced
// wrapper, guarding against accidental format drift.
func TestBuildTimestampFormat(t *testing.T) {
	fireTime := time.Date(2026, 5, 31, 9, 40, 0, 0, time.UTC)
	out := Build("", fireTime, "x")
	want := prefix + "2026-05-31 09:40 UTC:\n\nx"
	if out != want {
		t.Errorf("Build = %q, want %q", out, want)
	}
}

// Every fire of a job is a request in its own right. Two fires of the same job
// must never render identically, or the store's consecutive-duplicate filter
// would treat the later one as a repeat and drop it.
func TestBuild_EveryFireIsDistinct(t *testing.T) {
	first := time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC)
	a := Build("3f9a1c0d", first, "Run the morning report.")
	b := Build("3f9a1c0d", first.Add(24*time.Hour), "Run the morning report.")
	if a == b {
		t.Fatalf("two fires of one job a day apart rendered identically: %q", a)
	}
}
