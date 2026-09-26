package device

import (
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/channels"
)

func newClockedThrottle(start time.Time) (*authThrottle, *time.Time) {
	now := start
	t := newAuthThrottle()
	t.now = func() time.Time { return now }
	return t, &now
}

// failN records n failures from ip and returns the last call's result.
func failN(t *authThrottle, ip string, n int) (time.Duration, bool) {
	var d time.Duration
	var first bool
	for range n {
		d, first = t.fail(ip)
	}
	return d, first
}

// TestAuthThrottle_LocksAtThreshold: failures below the threshold do nothing;
// the threshold-th failure starts the minimum lockout, reported as the first.
func TestAuthThrottle_LocksAtThreshold(t *testing.T) {
	th, _ := newClockedThrottle(time.Unix(1_700_000_000, 0))
	if d, _ := failN(th, "10.0.0.1", channels.DeviceAuthFailThreshold-1); d != 0 {
		t.Fatalf("locked out before threshold: %v", d)
	}
	if th.retryAfter("10.0.0.1") != 0 {
		t.Fatal("retryAfter non-zero before threshold")
	}
	d, first := th.fail("10.0.0.1")
	if d != channels.DeviceAuthLockoutMin || !first {
		t.Fatalf("threshold failure: lockout=%v first=%v", d, first)
	}
	if got := th.retryAfter("10.0.0.1"); got != channels.DeviceAuthLockoutMin {
		t.Fatalf("retryAfter = %v, want %v", got, channels.DeviceAuthLockoutMin)
	}
	if th.retryAfter("10.0.0.2") != 0 {
		t.Fatal("another IP must not be locked out")
	}
}

// TestAuthThrottle_LockoutDoublesToCap: each lockout after the first doubles
// the previous one, capped at DeviceAuthLockoutMax, and only the first is
// reported as such.
func TestAuthThrottle_LockoutDoublesToCap(t *testing.T) {
	th, now := newClockedThrottle(time.Unix(1_700_000_000, 0))
	want := channels.DeviceAuthLockoutMin
	for i := range 8 {
		d, first := failN(th, "ip", channels.DeviceAuthFailThreshold)
		if d != want || first != (i == 0) {
			t.Fatalf("lockout %d: got %v first=%v, want %v first=%v", i, d, first, want, i == 0)
		}
		*now = now.Add(d) // lockout expires; failures resume immediately
		want = min(want*2, channels.DeviceAuthLockoutMax)
	}
	if want != channels.DeviceAuthLockoutMax {
		t.Fatalf("test did not reach the cap: %v", want)
	}
}

// TestAuthThrottle_WindowAndPrune: failures older than the window do not count,
// and an IP quiet for a window after its last lockout is forgotten, so its
// next lockout is a first one at the minimum length again.
func TestAuthThrottle_WindowAndPrune(t *testing.T) {
	th, now := newClockedThrottle(time.Unix(1_700_000_000, 0))
	failN(th, "ip", channels.DeviceAuthFailThreshold-1)
	*now = now.Add(channels.DeviceAuthFailWindow + time.Second)
	if d, _ := th.fail("ip"); d != 0 {
		t.Fatalf("stale failures counted toward lockout: %v", d)
	}

	// Two lockouts, then silence for a window past the second: forgotten.
	failN(th, "ip", channels.DeviceAuthFailThreshold-1)
	*now = now.Add(channels.DeviceAuthLockoutMin)
	d, _ := failN(th, "ip", channels.DeviceAuthFailThreshold)
	if d != 2*channels.DeviceAuthLockoutMin {
		t.Fatalf("second lockout = %v, want %v", d, 2*channels.DeviceAuthLockoutMin)
	}
	*now = now.Add(d + channels.DeviceAuthFailWindow + authPruneInterval)
	th.fail("other") // triggers the sweep
	if _, ok := th.byIP["ip"]; ok {
		t.Fatal("quiet IP not pruned")
	}
	d, first := failN(th, "ip", channels.DeviceAuthFailThreshold)
	if d != channels.DeviceAuthLockoutMin || !first {
		t.Fatalf("after prune: lockout=%v first=%v", d, first)
	}
}
