package device

import (
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/channels"
	"github.com/PivotLLM/ClawEh/config"
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

func mustExempt(t *testing.T, entries ...string) *config.LockoutExemptSet {
	t.Helper()
	set, err := config.CompileLockoutExempt(entries)
	if err != nil {
		t.Fatal(err)
	}
	return set
}

// TestAuthThrottle_Exempt: an exempt IP (listed, inside a listed CIDR, or
// loopback with an empty list) is never counted or locked out; any other IP
// locks at the threshold as before.
func TestAuthThrottle_Exempt(t *testing.T) {
	for _, tc := range []struct {
		name   string
		exempt *config.LockoutExemptSet
		ip     string
		locks  bool
	}{
		{"listed IP", mustExempt(t, "203.0.113.5"), "203.0.113.5", false},
		{"inside listed CIDR", mustExempt(t, "10.1.0.0/16"), "10.1.200.3", false},
		{"listed IPv6", mustExempt(t, "2001:db8::/32"), "2001:db8::7", false},
		{"loopback, empty list", mustExempt(t), "127.0.0.1", false},
		{"IPv6 loopback, nil list", nil, "::1", false},
		{"not listed", mustExempt(t, "10.1.0.0/16"), "10.2.0.1", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			th, _ := newClockedThrottle(time.Unix(1_700_000_000, 0))
			th.setExempt(tc.exempt)
			d, _ := failN(th, tc.ip, 4*channels.DeviceAuthFailThreshold)
			locked := th.retryAfter(tc.ip) > 0
			if locked != tc.locks {
				t.Fatalf("locked = %v, want %v (last lockout %v)", locked, tc.locks, d)
			}
			if !tc.locks && (d != 0 || len(th.byIP) != 0) {
				t.Fatalf("exempt IP was counted: lockout=%v entries=%d", d, len(th.byIP))
			}
		})
	}
}

// TestAuthThrottle_ExemptSwap: exempting a locked IP lets it straight in, and
// removing the exemption makes its failures count again.
func TestAuthThrottle_ExemptSwap(t *testing.T) {
	th, _ := newClockedThrottle(time.Unix(1_700_000_000, 0))
	const ip = "198.51.100.4"
	failN(th, ip, channels.DeviceAuthFailThreshold)
	if th.retryAfter(ip) == 0 {
		t.Fatal("not locked at the threshold")
	}
	th.setExempt(mustExempt(t, ip))
	if th.retryAfter(ip) != 0 {
		t.Fatal("exempted IP still locked out")
	}
	th.setExempt(nil)
	if th.retryAfter(ip) == 0 {
		t.Fatal("lock not in force again after the exemption was removed")
	}
}

// TestDeviceFactory_AppliesLockoutExempt: the channel factory, which the
// gateway runs again on every config reload, reads gateway.lockout_exempt, so
// a changed list replaces the previous one.
func TestDeviceFactory_AppliesLockoutExempt(t *testing.T) {
	t.Setenv("CLAW_HOME", t.TempDir())
	build := func(exempt []string) *DeviceChannel {
		t.Helper()
		cfg := config.DefaultConfig()
		cfg.Channels.Device.Enabled = true
		cfg.Channels.Device.Token = "shared"
		cfg.Gateway.LockoutExempt = exempt
		cm, err := channels.NewManager(cfg, bus.NewMessageBus(), nil)
		if err != nil {
			t.Fatal(err)
		}
		ch, ok := cm.Channel("device")
		if !ok {
			t.Fatal("device channel not built")
		}
		dc, ok := ch.(*DeviceChannel)
		if !ok {
			t.Fatalf("device channel is %T", ch)
		}
		t.Cleanup(func() {
			if err := dc.store.Close(); err != nil {
				t.Errorf("close store: %v", err)
			}
		})
		return dc
	}

	before := build([]string{"192.0.2.0/24"})
	after := build([]string{"198.51.100.7"})
	for _, tc := range []struct {
		dc     *DeviceChannel
		ip     string
		exempt bool
	}{
		{before, "192.0.2.9", true},
		{before, "198.51.100.7", false},
		{after, "192.0.2.9", false},
		{after, "198.51.100.7", true},
	} {
		failN(tc.dc.server.throttle, tc.ip, channels.DeviceAuthFailThreshold)
		if locked := tc.dc.server.throttle.retryAfter(tc.ip) > 0; locked == tc.exempt {
			t.Fatalf("%s: locked = %v with exempt = %v", tc.ip, locked, tc.exempt)
		}
	}
}
