package middleware

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/internal/admin"
)

// limiterKind abstracts over the two lock tables so the same cases run
// against both: fail records a failure that only touches this kind's key.
type limiterKind struct {
	name      string
	threshold int
	lockout   time.Duration
	account   bool
	// fail and attempt use key for this kind and a fresh key for the other,
	// so the other table never reaches its threshold.
	fail    func(l *LoginLimiter, key string, n int) []LockStart
	attempt func(l *LoginLimiter, key string, n int) time.Duration
	success func(l *LoginLimiter, key string)
}

func limiterKinds() []limiterKind {
	return []limiterKind{
		{
			name: "ip", threshold: LoginIPFailures, lockout: LoginIPLockout,
			fail: func(l *LoginLimiter, key string, n int) []LockStart {
				return l.Failure(key, fmt.Sprintf("user-%d", n))
			},
			attempt: func(l *LoginLimiter, key string, n int) time.Duration {
				return l.Attempt(key, fmt.Sprintf("user-%d", n))
			},
			success: func(l *LoginLimiter, key string) { l.Success(key, "someone") },
		},
		{
			name: "account", threshold: LoginUserFailures, lockout: LoginUserLockout, account: true,
			fail: func(l *LoginLimiter, key string, n int) []LockStart {
				return l.Failure(fmt.Sprintf("10.0.0.%d", n), key)
			},
			attempt: func(l *LoginLimiter, key string, n int) time.Duration {
				return l.Attempt(fmt.Sprintf("10.0.0.%d", n), key)
			},
			success: func(l *LoginLimiter, key string) { l.Success("192.0.2.99", key) },
		},
	}
}

func TestLoginLimiter_Constants(t *testing.T) {
	if LoginIPFailures != 10 || LoginIPLockout != 5*time.Minute ||
		LoginUserFailures != 10 || LoginUserLockout != 10*time.Minute ||
		loginFailureWindow != 10*time.Minute {
		t.Fatal("login lockout constants differ from the documented values")
	}
}

func TestLoginLimiter_LocksAtThresholdNotBefore(t *testing.T) {
	for _, k := range limiterKinds() {
		t.Run(k.name, func(t *testing.T) {
			clock := newClock()
			l := NewLoginLimiter(clock.now)
			const key = "203.0.113.9"
			for i := 1; i < k.threshold; i++ {
				if started := k.fail(l, key, i); len(started) != 0 {
					t.Fatalf("failure %d started %+v", i, started)
				}
				if wait := k.attempt(l, key, 100+i); wait != 0 {
					t.Fatalf("locked after %d failures: %v", i, wait)
				}
			}
			started := k.fail(l, key, k.threshold)
			if len(started) != 1 || started[0].Key != key || started[0].Account != k.account || started[0].For != k.lockout {
				t.Fatalf("failure %d started %+v, want one %s lock on %s", k.threshold, started, k.name, key)
			}
			if wait := k.attempt(l, key, 999); wait != k.lockout {
				t.Fatalf("attempt while locked: wait = %v, want %v", wait, k.lockout)
			}
			if wait := k.attempt(l, "198.51.100.1", 998); wait != 0 {
				t.Fatalf("another key was locked by this one's failures: %v", wait)
			}
		})
	}
}

func TestLoginLimiter_FailuresOutsideWindowDoNotCount(t *testing.T) {
	for _, k := range limiterKinds() {
		t.Run(k.name, func(t *testing.T) {
			clock := newClock()
			l := NewLoginLimiter(clock.now)
			const key = "k"
			for i := 1; i < k.threshold; i++ {
				k.fail(l, key, i)
			}
			clock.advance(loginFailureWindow + time.Second)
			if started := k.fail(l, key, k.threshold); len(started) != 0 {
				t.Fatalf("a failure after the window started a lock: %+v", started)
			}
		})
	}
}

func TestLoginLimiter_AttemptWhileLockedExtendsLock(t *testing.T) {
	for _, k := range limiterKinds() {
		t.Run(k.name, func(t *testing.T) {
			clock := newClock()
			l := NewLoginLimiter(clock.now)
			const key = "k"
			for i := 1; i <= k.threshold; i++ {
				k.fail(l, key, i)
			}
			// Keep knocking just before the lock would end: it never ends,
			// however long this goes on (no cap).
			for i := range 20 {
				clock.advance(k.lockout - time.Second)
				if wait := k.attempt(l, key, i); wait != k.lockout {
					t.Fatalf("attempt %d while locked: wait = %v, want the full %v", i, wait, k.lockout)
				}
			}
			// A full lockout period of silence ends it.
			clock.advance(k.lockout + time.Second)
			if wait := k.attempt(l, key, 50); wait != 0 {
				t.Fatalf("still locked after %v of silence: %v", k.lockout, wait)
			}
		})
	}
}

func TestLoginLimiter_ExpiryAfterSilence(t *testing.T) {
	for _, k := range limiterKinds() {
		t.Run(k.name, func(t *testing.T) {
			clock := newClock()
			l := NewLoginLimiter(clock.now)
			const key = "k"
			for i := 1; i <= k.threshold; i++ {
				k.fail(l, key, i)
			}
			clock.advance(k.lockout - time.Second)
			if wait := k.attempt(l, key, 0); wait == 0 {
				t.Fatal("unlocked before the lockout elapsed")
			}
			clock.advance(k.lockout + time.Second)
			if wait := k.attempt(l, key, 0); wait != 0 {
				t.Fatalf("still locked after silence: %v", wait)
			}
			// The lock consumed the failures: one more does not relock, and a
			// fresh run of failures starts a new lock (reported again).
			if started := k.fail(l, key, 1); len(started) != 0 {
				t.Fatalf("first failure after expiry relocked: %+v", started)
			}
			var started []LockStart
			for i := 2; i <= k.threshold; i++ {
				started = k.fail(l, key, i)
			}
			if len(started) != 1 {
				t.Fatalf("a new run of failures after expiry did not start a lock: %+v", started)
			}
		})
	}
}

func TestLoginLimiter_SuccessClearsFailures(t *testing.T) {
	for _, k := range limiterKinds() {
		t.Run(k.name, func(t *testing.T) {
			clock := newClock()
			l := NewLoginLimiter(clock.now)
			const key = "k"
			for i := 1; i < k.threshold; i++ {
				k.fail(l, key, i)
			}
			k.success(l, key)
			if started := k.fail(l, key, k.threshold); len(started) != 0 {
				t.Fatalf("failures survived a successful login: %+v", started)
			}
		})
	}
}

func TestLoginLimiter_BothLocksStartAndApply(t *testing.T) {
	clock := newClock()
	l := NewLoginLimiter(clock.now)
	var started []LockStart
	for range LoginIPFailures {
		started = l.Failure("203.0.113.1", "alice")
	}
	if len(started) != 2 || started[0].Account || !started[1].Account {
		t.Fatalf("started = %+v, want an address lock and an account lock", started)
	}
	if wait := l.Attempt("203.0.113.1", "bob"); wait != LoginIPLockout {
		t.Fatalf("address lock wait = %v", wait)
	}
	if wait := l.Attempt("198.51.100.2", "alice"); wait != LoginUserLockout {
		t.Fatalf("account lock wait = %v", wait)
	}
	if wait := l.Attempt("203.0.113.1", "alice"); wait != LoginUserLockout {
		t.Fatalf("both locks wait = %v, want the longer", wait)
	}
}

func TestLoginLimiter_SyncCredentialsClearsEverything(t *testing.T) {
	clock := newClock()
	l := NewLoginLimiter(clock.now)
	if l.SyncCredentials(0) {
		t.Fatal("unchanged generation cleared")
	}
	for range LoginIPFailures {
		l.Failure("203.0.113.1", "alice")
	}
	if l.SyncCredentials(0) {
		t.Fatal("same generation cleared")
	}
	if wait := l.Attempt("203.0.113.1", "alice"); wait == 0 {
		t.Fatal("locks cleared by an unchanged generation")
	}
	if !l.SyncCredentials(1) {
		t.Fatal("a new generation did not clear")
	}
	if wait := l.Attempt("203.0.113.1", "alice"); wait != 0 {
		t.Fatalf("locked after the credentials changed: %v", wait)
	}
}

func TestLoginLimiter_QuietEntriesArePruned(t *testing.T) {
	clock := newClock()
	l := NewLoginLimiter(clock.now)
	for i := range 50 {
		l.Failure(fmt.Sprintf("10.1.0.%d", i), fmt.Sprintf("user-%d", i))
	}
	for range LoginIPFailures {
		l.Failure("203.0.113.1", "alice")
	}
	clock.advance(loginFailureWindow + time.Second)
	l.Attempt("192.0.2.1", "x") // any call sweeps once the prune interval has passed
	l.mu.Lock()
	ips, users := len(l.ips.entries), len(l.users.entries)
	l.mu.Unlock()
	if ips != 0 || users != 0 {
		t.Fatalf("after the window and the locks ended: %d addresses, %d usernames remain", ips, users)
	}
}

func TestAuthStore_RefreshGeneration(t *testing.T) {
	path := writeCreds(t)
	s := NewAuthStore(path)
	gen := s.Refresh()
	if again := s.Refresh(); again != gen {
		t.Fatalf("generation moved without a file change: %d -> %d", gen, again)
	}

	// `claw admin` rewrites the file: Refresh reloads it at once, with no
	// Watch poll, and the generation moves.
	if err := admin.Write(path, testUser, "a different fine password"); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, time.Now(), time.Now().Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	next := s.Refresh()
	if next == gen {
		t.Fatal("generation did not move after the credentials file changed")
	}
	if _, ok := s.Login(testUser, "a different fine password"); !ok {
		t.Fatal("new password rejected right after Refresh")
	}

	// Same account, only the mtime changes: still a new generation.
	if err := os.Chtimes(path, time.Now(), time.Now().Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	if s.Refresh() == next {
		t.Fatal("generation did not move after the mtime changed")
	}
}
