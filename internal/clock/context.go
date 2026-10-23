// ClawEh
// License: MIT

package clock

import (
	"context"
	"sync"
	"time"
)

// WithTimeout is context.WithTimeout on the fake clock.
func (f *Fake) WithTimeout(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return f.WithDeadline(parent, f.Now().Add(d))
}

// WithDeadline is context.WithDeadline on the fake clock: the context ends
// with context.DeadlineExceeded, its cause too, once the clock reaches
// deadline (at once when it is already there), or with its parent's error
// and cause when the parent ends first. Deadline reports the parent's
// deadline when that is earlier, as context.WithDeadline does.
func (f *Fake) WithDeadline(parent context.Context, deadline time.Time) (context.Context, context.CancelFunc) {
	// inner carries the cause: context.Cause finds it through Value. done
	// is the context's own channel, so a context derived from this one
	// learns of its end through Err (context.DeadlineExceeded) rather than
	// as a child of inner (context.Canceled).
	inner, cancelInner := context.WithCancelCause(parent)
	c := &fakeDeadlineCtx{inner: inner, cancelInner: cancelInner, deadline: deadline, done: make(chan struct{})}
	if pd, ok := parent.Deadline(); ok && pd.Before(deadline) {
		c.deadline = pd
	}
	cancel := func() { c.end(context.Canceled, context.Canceled) }
	if err := parent.Err(); err != nil {
		c.end(err, context.Cause(parent))
		return c, cancel
	}
	if !deadline.After(f.Now()) {
		c.end(context.DeadlineExceeded, context.DeadlineExceeded)
		return c, cancel
	}
	stopParent := context.AfterFunc(parent, func() { c.end(parent.Err(), context.Cause(parent)) })
	timer := f.AfterFunc(f.Until(deadline), func() { c.end(context.DeadlineExceeded, context.DeadlineExceeded) })
	c.mu.Lock()
	ended := c.err != nil
	c.timer, c.stopParent = timer, stopParent
	c.mu.Unlock()
	if ended { // it ended while the two were being set up
		timer.Stop()
		stopParent()
	}
	return c, cancel
}

// fakeDeadlineCtx is a context with a deadline on a Fake.
type fakeDeadlineCtx struct {
	inner       context.Context
	cancelInner context.CancelCauseFunc
	deadline    time.Time
	done        chan struct{}
	once        sync.Once
	mu          sync.Mutex
	err         error
	timer       Timer
	stopParent  func() bool
}

func (c *fakeDeadlineCtx) Deadline() (time.Time, bool) { return c.deadline, true }
func (c *fakeDeadlineCtx) Done() <-chan struct{}       { return c.done }
func (c *fakeDeadlineCtx) Value(key any) any           { return c.inner.Value(key) }

func (c *fakeDeadlineCtx) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// end records err as the context's error and cause as its cause, and
// closes it, once.
func (c *fakeDeadlineCtx) end(err, cause error) {
	c.once.Do(func() {
		c.cancelInner(cause) // before done closes: a waiter woken by it sees the cause
		c.mu.Lock()
		c.err = err
		timer, stopParent := c.timer, c.stopParent
		c.mu.Unlock()
		close(c.done)
		if timer != nil {
			timer.Stop()
		}
		if stopParent != nil {
			stopParent()
		}
	})
}
