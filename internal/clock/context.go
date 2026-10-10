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
// with context.DeadlineExceeded once the clock reaches deadline, or with
// its parent's error when the parent ends first.
func (f *Fake) WithDeadline(parent context.Context, deadline time.Time) (context.Context, context.CancelFunc) {
	c := &fakeDeadlineCtx{parent: parent, deadline: deadline, done: make(chan struct{})}
	if err := parent.Err(); err != nil {
		c.end(err)
		return c, func() { c.end(context.Canceled) }
	}
	stopParent := context.AfterFunc(parent, func() { c.end(parent.Err()) })
	timer := f.AfterFunc(f.Until(deadline), func() { c.end(context.DeadlineExceeded) })
	c.mu.Lock()
	ended := c.err != nil
	c.timer, c.stopParent = timer, stopParent
	c.mu.Unlock()
	if ended { // it ended while the two were being set up
		timer.Stop()
		stopParent()
	}
	return c, func() { c.end(context.Canceled) }
}

// fakeDeadlineCtx is a context with a deadline on a Fake. It keeps a done
// channel of its own, so contexts derived from it learn of its end through
// Err (context.DeadlineExceeded), not through a parent cancelCtx.
type fakeDeadlineCtx struct {
	parent     context.Context
	deadline   time.Time
	done       chan struct{}
	once       sync.Once
	mu         sync.Mutex
	err        error
	timer      Timer
	stopParent func() bool
}

func (c *fakeDeadlineCtx) Deadline() (time.Time, bool) { return c.deadline, true }
func (c *fakeDeadlineCtx) Done() <-chan struct{}       { return c.done }
func (c *fakeDeadlineCtx) Value(key any) any           { return c.parent.Value(key) }

func (c *fakeDeadlineCtx) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// end records err as the context's error and closes it, once.
func (c *fakeDeadlineCtx) end(err error) {
	c.once.Do(func() {
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
