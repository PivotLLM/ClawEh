package device

import (
	"slices"
	"sync"
	"time"

	"github.com/PivotLLM/ClawEh/channels"
)

// authPruneInterval bounds how often the throttle sweeps forgotten IPs.
const authPruneInterval = time.Minute

// authThrottle counts failed gateway authentications per client IP and locks
// an IP out once it fails channels.DeviceAuthFailThreshold times inside
// channels.DeviceAuthFailWindow. Lockouts double from DeviceAuthLockoutMin up
// to DeviceAuthLockoutMax while the IP keeps failing; an IP that stays quiet
// for a window after its last failure or lockout is forgotten.
type authThrottle struct {
	mu        sync.Mutex
	byIP      map[string]*authFailState
	now       func() time.Time
	lastPrune time.Time
}

type authFailState struct {
	failures    []time.Time // failures since the last lockout, oldest first
	lockouts    int         // lockouts so far; sets the next lockout's length
	lockedUntil time.Time
}

func newAuthThrottle() *authThrottle {
	return &authThrottle{byIP: map[string]*authFailState{}, now: time.Now}
}

// retryAfter reports how long ip stays locked out; zero when it may try.
func (t *authThrottle) retryAfter(ip string) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	if st := t.byIP[ip]; st != nil {
		if d := st.lockedUntil.Sub(t.now()); d > 0 {
			return d
		}
	}
	return 0
}

// fail records one failed authentication from ip. When this failure starts a
// lockout it returns the lockout's length and whether it is ip's first.
func (t *authThrottle) fail(ip string) (lockout time.Duration, first bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	t.prune(now)

	st := t.byIP[ip]
	if st == nil {
		st = &authFailState{}
		t.byIP[ip] = st
	}
	cutoff := now.Add(-channels.DeviceAuthFailWindow)
	st.failures = slices.DeleteFunc(st.failures, func(f time.Time) bool { return !f.After(cutoff) })
	st.failures = append(st.failures, now)
	if len(st.failures) < channels.DeviceAuthFailThreshold {
		return 0, false
	}

	lockout = channels.DeviceAuthLockoutMin
	for i := 0; i < st.lockouts && lockout < channels.DeviceAuthLockoutMax; i++ {
		lockout *= 2
	}
	lockout = min(lockout, channels.DeviceAuthLockoutMax)
	st.lockouts++
	st.lockedUntil = now.Add(lockout)
	st.failures = nil
	return lockout, st.lockouts == 1
}

// prune forgets IPs whose last failure and lockout are both more than a window
// old. Runs at most once per authPruneInterval; the caller holds mu.
func (t *authThrottle) prune(now time.Time) {
	if now.Sub(t.lastPrune) < authPruneInterval {
		return
	}
	t.lastPrune = now
	for ip, st := range t.byIP {
		last := st.lockedUntil
		if n := len(st.failures); n > 0 && st.failures[n-1].After(last) {
			last = st.failures[n-1]
		}
		if !last.Add(channels.DeviceAuthFailWindow).After(now) {
			delete(t.byIP, ip)
		}
	}
}
