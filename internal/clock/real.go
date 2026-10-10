// ClawEh
// License: MIT

// Package clock is the time source code reads instead of the time package
// when its timing must be testable: Real is the system clock every running
// service uses, and Fake (fake.go) is a clock tests move by hand.
//
// A component takes a Clock as a field or option and calls Or on it, so a
// nil Clock means Real and production wiring never has to name one.
package clock

import (
	"context"
	"time"
)

// Clock is the subset of the time package that timing code needs.
type Clock interface {
	Now() time.Time
	Since(t time.Time) time.Duration
	Until(t time.Time) time.Duration
	Sleep(d time.Duration)
	After(d time.Duration) <-chan time.Time
	NewTimer(d time.Duration) Timer
	NewTicker(d time.Duration) Ticker
	AfterFunc(d time.Duration, f func()) Timer
	// WithTimeout is context.WithTimeout on this clock.
	WithTimeout(parent context.Context, d time.Duration) (context.Context, context.CancelFunc)
	// WithDeadline is context.WithDeadline on this clock.
	WithDeadline(parent context.Context, deadline time.Time) (context.Context, context.CancelFunc)
}

// Timer is a *time.Timer behind an interface; C replaces the field.
type Timer interface {
	C() <-chan time.Time
	Stop() bool
	Reset(d time.Duration) bool
}

// Ticker is a *time.Ticker behind an interface; C replaces the field.
type Ticker interface {
	C() <-chan time.Time
	Stop()
	Reset(d time.Duration)
}

// Real is the system clock.
var Real Clock = realClock{}

// Or returns c, or Real when c is nil.
func Or(c Clock) Clock {
	if c == nil {
		return Real
	}
	return c
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) Since(t time.Time) time.Duration        { return time.Since(t) }
func (realClock) Until(t time.Time) time.Duration        { return time.Until(t) }
func (realClock) Sleep(d time.Duration)                  { time.Sleep(d) }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }
func (realClock) NewTimer(d time.Duration) Timer         { return realTimer{time.NewTimer(d)} }
func (realClock) NewTicker(d time.Duration) Ticker       { return realTicker{time.NewTicker(d)} }

func (realClock) AfterFunc(d time.Duration, f func()) Timer {
	return realTimer{time.AfterFunc(d, f)}
}

func (realClock) WithTimeout(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, d)
}

func (realClock) WithDeadline(parent context.Context, deadline time.Time) (context.Context, context.CancelFunc) {
	return context.WithDeadline(parent, deadline)
}

type realTimer struct{ t *time.Timer }

func (r realTimer) C() <-chan time.Time        { return r.t.C }
func (r realTimer) Stop() bool                 { return r.t.Stop() }
func (r realTimer) Reset(d time.Duration) bool { return r.t.Reset(d) }

type realTicker struct{ t *time.Ticker }

func (r realTicker) C() <-chan time.Time   { return r.t.C }
func (r realTicker) Stop()                 { r.t.Stop() }
func (r realTicker) Reset(d time.Duration) { r.t.Reset(d) }
