package middleware

import (
	"sync"
	"time"
)

// Login lockout. Failed logins are counted per client address and per
// submitted username over loginFailureWindow. Reaching the threshold locks
// that address or username; while it is locked every attempt is refused and
// restarts the lock at its full length, so a lock ends only after a full
// lockout period with no attempts at all. There is no escalation and no cap.
const (
	// LoginIPFailures is how many failed logins from one client address
	// inside loginFailureWindow lock that address.
	LoginIPFailures = 10
	// LoginIPLockout is how long a locked address stays locked after its
	// last attempt.
	LoginIPLockout = 5 * time.Minute
	// LoginUserFailures is how many failed logins against one username (as
	// submitted, whether or not the account exists) inside
	// loginFailureWindow lock that username.
	LoginUserFailures = 10
	// LoginUserLockout is how long a locked username stays locked after its
	// last attempt.
	LoginUserLockout = 10 * time.Minute

	// loginFailureWindow is how far back failures are counted.
	loginFailureWindow = 10 * time.Minute
	// loginPruneInterval bounds how often the tables are swept for quiet
	// entries, so a flood of distinct usernames does not make every attempt
	// walk the whole map.
	loginPruneInterval = time.Minute
)

// LockStart describes a lockout that a failure has just started.
type LockStart struct {
	Account bool   // true for a username lock, false for a client-address lock
	Key     string // the client address or the username
	For     time.Duration
}

type lockEntry struct {
	failures    []time.Time
	lockedUntil time.Time
}

// lockTable is the failure and lock state for one kind of key.
type lockTable struct {
	threshold int
	lockout   time.Duration
	entries   map[string]*lockEntry
}

func newLockTable(threshold int, lockout time.Duration) *lockTable {
	return &lockTable{threshold: threshold, lockout: lockout, entries: make(map[string]*lockEntry)}
}

// touchLocked reports whether key is locked at now and, if it is, restarts
// the lock from now.
func (t *lockTable) touchLocked(key string, now time.Time) bool {
	e, ok := t.entries[key]
	if !ok || !e.lockedUntil.After(now) {
		return false
	}
	e.lockedUntil = now.Add(t.lockout)
	return true
}

// failure records a failure for key and reports whether it started a lock.
func (t *lockTable) failure(key string, now time.Time) bool {
	e, ok := t.entries[key]
	if !ok {
		e = &lockEntry{}
		t.entries[key] = e
	}
	e.failures = append(dropBefore(e.failures, now.Add(-loginFailureWindow)), now)
	if len(e.failures) < t.threshold {
		return false
	}
	e.failures = nil
	e.lockedUntil = now.Add(t.lockout)
	return true
}

// prune drops entries that are not locked and have no failure inside the
// window: they carry no state.
func (t *lockTable) prune(now time.Time) {
	cutoff := now.Add(-loginFailureWindow)
	for k, e := range t.entries {
		e.failures = dropBefore(e.failures, cutoff)
		if len(e.failures) == 0 && !e.lockedUntil.After(now) {
			delete(t.entries, k)
		}
	}
}

// LoginLimiter applies the login lockout above, keyed by client address and
// by submitted username. It is safe for concurrent use.
type LoginLimiter struct {
	now func() time.Time

	mu        sync.Mutex
	ips       *lockTable
	users     *lockTable
	lastPrune time.Time
	credGen   uint64
}

// NewLoginLimiter creates a limiter; now is the clock (time.Now in
// production, injected in tests).
func NewLoginLimiter(now func() time.Time) *LoginLimiter {
	if now == nil {
		now = time.Now
	}
	return &LoginLimiter{
		now:   now,
		ips:   newLockTable(LoginIPFailures, LoginIPLockout),
		users: newLockTable(LoginUserFailures, LoginUserLockout),
	}
}

// SyncCredentials clears every lock and failure count when gen differs from
// the credentials generation seen by the previous call, so writing the
// credentials file (`claw admin`) lets the operator straight back in. It
// reports whether it cleared anything.
func (l *LoginLimiter) SyncCredentials(gen uint64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if gen == l.credGen {
		return false
	}
	l.credGen = gen
	l.ips = newLockTable(LoginIPFailures, LoginIPLockout)
	l.users = newLockTable(LoginUserFailures, LoginUserLockout)
	return true
}

// Attempt is called before the password is checked. When ip or username is
// locked it restarts each lock that applies and returns how long the caller
// must now wait; 0 means the attempt may proceed.
func (l *LoginLimiter) Attempt(ip, username string) time.Duration {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pruneLocked(now)
	var wait time.Duration
	if l.ips.touchLocked(ip, now) {
		wait = l.ips.lockout
	}
	if l.users.touchLocked(username, now) {
		wait = max(wait, l.users.lockout)
	}
	return wait
}

// Failure records a failed login for ip and username and returns the locks it
// started (none, one or both).
func (l *LoginLimiter) Failure(ip, username string) []LockStart {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pruneLocked(now)
	var started []LockStart
	if l.ips.failure(ip, now) {
		started = append(started, LockStart{Key: ip, For: l.ips.lockout})
	}
	if l.users.failure(username, now) {
		started = append(started, LockStart{Account: true, Key: username, For: l.users.lockout})
	}
	return started
}

// Success records a successful login, clearing the failure counts of ip and
// username.
func (l *LoginLimiter) Success(ip, username string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.ips.entries, ip)
	delete(l.users.entries, username)
}

// pruneLocked sweeps quiet entries at most once per loginPruneInterval.
// Called with mu held.
func (l *LoginLimiter) pruneLocked(now time.Time) {
	if now.Sub(l.lastPrune) < loginPruneInterval {
		return
	}
	l.lastPrune = now
	l.ips.prune(now)
	l.users.prune(now)
}

func dropBefore(ts []time.Time, cutoff time.Time) []time.Time {
	i := 0
	for i < len(ts) && ts[i].Before(cutoff) {
		i++
	}
	return ts[i:]
}
