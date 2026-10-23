// ClawEh
// License: MIT

package clock

import (
	"context"
	"slices"
	"sync"
	"time"
)

// Fake is a Clock that moves only when a test calls Advance or Set.
// Timers, tickers, sleeps and context deadlines on it fire in time order
// as it passes them. A test that needs code to be waiting before it moves
// the clock calls BlockUntil, so it waits on that
// event instead of sleeping.
//
// AfterFunc functions run on the goroutine calling Advance or Set, after
// the clock has reached their time, so they must not block; one due at once
// (d <= 0) runs on a goroutine of its own, as with time.AfterFunc.
type Fake struct {
	mu      sync.Mutex
	now     time.Time
	waiters []*fakeTimer
	// changed is closed and replaced whenever waiters grows, waking
	// BlockUntil.
	changed chan struct{}
}

// NewFake returns a Fake reading start.
func NewFake(start time.Time) *Fake {
	return &Fake{now: start, changed: make(chan struct{})}
}

// Now returns the fake time.
func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// Since is Now().Sub(t).
func (f *Fake) Since(t time.Time) time.Duration { return f.Now().Sub(t) }

// Until is t.Sub(Now()).
func (f *Fake) Until(t time.Time) time.Duration { return t.Sub(f.Now()) }

// Sleep blocks until the clock has moved d on.
func (f *Fake) Sleep(d time.Duration) { <-f.NewTimer(d).C() }

// After is NewTimer(d).C().
func (f *Fake) After(d time.Duration) <-chan time.Time { return f.NewTimer(d).C() }

// NewTimer returns a timer that fires once the clock has moved d on.
func (f *Fake) NewTimer(d time.Duration) Timer {
	t := &fakeTimer{f: f, ch: make(chan time.Time, 1)}
	t.Reset(d)
	return t
}

// AfterFunc runs fn once the clock has moved d on.
func (f *Fake) AfterFunc(d time.Duration, fn func()) Timer {
	t := &fakeTimer{f: f, fn: fn}
	t.Reset(d)
	return t
}

// NewTicker returns a ticker that ticks every d of fake time. Like a
// time.Ticker it keeps one tick and drops the rest when it is not read.
func (f *Fake) NewTicker(d time.Duration) Ticker {
	if d <= 0 {
		panic("clock: non-positive interval for NewTicker")
	}
	t := &fakeTimer{f: f, ch: make(chan time.Time, 1), period: d}
	t.Reset(d)
	return &fakeTicker{t}
}

// Advance moves the clock d on, firing everything due on the way in time
// order.
func (f *Fake) Advance(d time.Duration) {
	f.Set(f.Now().Add(d))
}

// Set moves the clock to t (never back), firing everything due on the way
// in time order.
func (f *Fake) Set(t time.Time) {
	for {
		f.mu.Lock()
		next := f.nextDueLocked(t)
		if next == nil {
			if t.After(f.now) {
				f.now = t
			}
			f.mu.Unlock()
			return
		}
		if next.when.After(f.now) {
			f.now = next.when
		}
		fn := next.fireLocked()
		f.mu.Unlock()
		if fn != nil {
			fn()
		}
	}
}

// Waiters reports how many timers, tickers, sleeps and context deadlines
// are waiting on the clock.
func (f *Fake) Waiters() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.waiters)
}

// BlockUntil waits until at least n timers, tickers, sleeps or context
// deadlines are waiting on the clock, giving up with ctx's error when ctx
// ends first.
func (f *Fake) BlockUntil(ctx context.Context, n int) error {
	for {
		f.mu.Lock()
		if len(f.waiters) >= n {
			f.mu.Unlock()
			return nil
		}
		changed := f.changed
		f.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// nextDueLocked returns the waiter due first at or before t, nil when none
// is.
func (f *Fake) nextDueLocked(t time.Time) *fakeTimer {
	var next *fakeTimer
	for _, w := range f.waiters {
		if !w.when.After(t) && (next == nil || w.when.Before(next.when)) {
			next = w
		}
	}
	return next
}

func (f *Fake) addLocked(t *fakeTimer) {
	f.waiters = append(f.waiters, t)
	close(f.changed)
	f.changed = make(chan struct{})
}

// removeLocked drops t from the waiters, reporting whether it was there.
func (f *Fake) removeLocked(t *fakeTimer) bool {
	i := slices.Index(f.waiters, t)
	if i < 0 {
		return false
	}
	f.waiters = slices.Delete(f.waiters, i, i+1)
	return true
}

// fakeTimer is a timer (ch), a function timer (fn) or, with a period, a
// ticker.
type fakeTimer struct {
	f      *Fake
	when   time.Time
	period time.Duration
	ch     chan time.Time
	fn     func()
}

// fireLocked delivers one firing: a tick or timer value is sent without
// blocking (one is kept, the rest dropped), and fn is returned for the
// caller to run once the lock is released. A ticker is re-armed.
func (t *fakeTimer) fireLocked() func() {
	if t.period > 0 {
		t.when = t.when.Add(t.period)
	} else {
		t.f.removeLocked(t)
	}
	if t.ch != nil {
		select {
		case t.ch <- t.f.now:
		default:
		}
	}
	return t.fn
}

func (t *fakeTimer) C() <-chan time.Time { return t.ch }

// Stop disarms the timer and drops a value it has not delivered, as a
// time.Timer does since Go 1.23.
func (t *fakeTimer) Stop() bool {
	t.f.mu.Lock()
	defer t.f.mu.Unlock()
	t.drainLocked()
	return t.f.removeLocked(t)
}

// Reset re-arms the timer d from now.
func (t *fakeTimer) Reset(d time.Duration) bool {
	t.f.mu.Lock()
	t.drainLocked()
	active := t.f.removeLocked(t)
	t.when = t.f.now.Add(d)
	t.f.addLocked(t)
	var fn func()
	if d <= 0 {
		fn = t.fireLocked()
	}
	t.f.mu.Unlock()
	if fn != nil {
		go fn() // as time.AfterFunc does; Reset may be called with a lock fn needs
	}
	return active
}

func (t *fakeTimer) drainLocked() {
	if t.ch == nil {
		return
	}
	select {
	case <-t.ch:
	default:
	}
}

type fakeTicker struct{ t *fakeTimer }

func (k *fakeTicker) C() <-chan time.Time { return k.t.ch }
func (k *fakeTicker) Stop()               { k.t.Stop() }

func (k *fakeTicker) Reset(d time.Duration) {
	if d <= 0 {
		panic("clock: non-positive interval for Ticker.Reset")
	}
	k.t.f.mu.Lock()
	k.t.period = d
	k.t.f.mu.Unlock()
	k.t.Reset(d)
}
