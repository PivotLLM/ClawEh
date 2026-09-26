package middleware

import (
	"sync"
	"time"
)

// Login backoff. Per client address: LoginFailuresPerIP failures inside
// LoginFailureWindow lock that address for LoginLockBase, doubling on each
// further lockout up to LoginLockMax. Across all addresses: LoginGlobalFailures
// failures inside the window lock every login for LoginGlobalLock, so a
// distributed guess is slowed even when no single address trips its own limit.
const (
	LoginFailureWindow  = 10 * time.Minute
	LoginFailuresPerIP  = 5
	LoginLockBase       = time.Minute
	LoginLockMax        = time.Hour
	LoginGlobalFailures = 100
	LoginGlobalLock     = 60 * time.Second

	// loginEntryTTL is how long a quiet address is remembered (so its lockout
	// doubling and "already alerted" state survive a pause between bursts).
	loginEntryTTL = 24 * time.Hour
)

type loginEntry struct {
	failures    []time.Time
	lockedUntil time.Time
	lockouts    int
	alerted     bool
	lastSeen    time.Time
}

// LoginLimiter tracks failed logins by client address and applies the
// backoff above. It is safe for concurrent use.
type LoginLimiter struct {
	now func() time.Time

	mu           sync.Mutex
	ips          map[string]*loginEntry
	global       []time.Time
	globalLocked time.Time
}

// NewLoginLimiter creates a limiter; now is the clock (time.Now in
// production, injected in tests).
func NewLoginLimiter(now func() time.Time) *LoginLimiter {
	if now == nil {
		now = time.Now
	}
	return &LoginLimiter{now: now, ips: make(map[string]*loginEntry)}
}

// Check reports how long ip must wait before a login attempt is accepted; 0
// means it may try now.
func (l *LoginLimiter) Check(ip string) time.Duration {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pruneLocked(now)
	if wait := l.globalLocked.Sub(now); wait > 0 {
		return wait
	}
	if e, ok := l.ips[ip]; ok {
		if wait := e.lockedUntil.Sub(now); wait > 0 {
			return wait
		}
	}
	return 0
}

// Failure records a failed login from ip. It returns the lock this failure
// triggered (0 when none) and whether it is the first lockout recorded for ip,
// which is when the operator is alerted.
func (l *LoginLimiter) Failure(ip string) (locked time.Duration, firstLockout bool) {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pruneLocked(now)

	l.global = append(l.global, now)
	if len(l.global) >= LoginGlobalFailures && l.globalLocked.Before(now) {
		l.globalLocked = now.Add(LoginGlobalLock)
		l.global = l.global[:0]
		locked = LoginGlobalLock
	}

	e, ok := l.ips[ip]
	if !ok {
		e = &loginEntry{}
		l.ips[ip] = e
	}
	e.lastSeen = now
	e.failures = append(e.failures, now)
	if len(e.failures) < LoginFailuresPerIP {
		return locked, false
	}

	e.failures = e.failures[:0]
	e.lockouts++
	d := LoginLockBase
	for i := 1; i < e.lockouts && d < LoginLockMax; i++ {
		d *= 2
	}
	d = min(d, LoginLockMax)
	e.lockedUntil = now.Add(d)
	firstLockout = !e.alerted
	e.alerted = true
	return max(locked, d), firstLockout
}

// Success records a successful login from ip, clearing its failure history
// and lockout escalation.
func (l *LoginLimiter) Success(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e, ok := l.ips[ip]; ok {
		e.failures = e.failures[:0]
		e.lockouts = 0
		e.lockedUntil = time.Time{}
		e.lastSeen = l.now()
	}
}

// pruneLocked drops failures outside the window and addresses quiet for
// loginEntryTTL. Called with mu held.
func (l *LoginLimiter) pruneLocked(now time.Time) {
	cutoff := now.Add(-LoginFailureWindow)
	l.global = dropBefore(l.global, cutoff)
	for ip, e := range l.ips {
		e.failures = dropBefore(e.failures, cutoff)
		if now.Sub(e.lastSeen) > loginEntryTTL && !e.lockedUntil.After(now) {
			delete(l.ips, ip)
		}
	}
}

func dropBefore(ts []time.Time, cutoff time.Time) []time.Time {
	i := 0
	for i < len(ts) && ts[i].Before(cutoff) {
		i++
	}
	return ts[i:]
}
