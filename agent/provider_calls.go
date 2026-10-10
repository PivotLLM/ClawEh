// ClawEh
// License: MIT

package agent

import (
	"reflect"
	"sync"

	"github.com/PivotLLM/ClawEh/providers"
)

// providerCalls counts the model calls in flight on each provider, so a
// reload can close the provider it replaced once the calls still using it
// have finished, without waiting on calls to the new one. The zero value is
// ready to use.
type providerCalls struct {
	mu    sync.Mutex
	calls map[providers.LLMProvider]*providerCallCount
}

// providerCallCount is one provider's calls in flight; idle is closed when
// the last one ends.
type providerCallCount struct {
	n    int
	idle chan struct{}
}

// begin counts a call on p and returns the function that ends it. A
// provider that cannot be a map key (not comparable) is not counted.
func (pc *providerCalls) begin(p providers.LLMProvider) (end func()) {
	if !countable(p) {
		return func() {}
	}
	pc.mu.Lock()
	defer pc.mu.Unlock()
	if pc.calls == nil {
		pc.calls = make(map[providers.LLMProvider]*providerCallCount)
	}
	c := pc.calls[p]
	if c == nil {
		c = &providerCallCount{idle: make(chan struct{})}
		pc.calls[p] = c
	}
	c.n++
	var once sync.Once
	return func() {
		once.Do(func() {
			pc.mu.Lock()
			defer pc.mu.Unlock()
			c.n--
			if c.n == 0 {
				close(c.idle)
				delete(pc.calls, p)
			}
		})
	}
}

// idle returns a channel that is closed once no call is in flight on p:
// already closed when none is.
func (pc *providerCalls) idle(p providers.LLMProvider) <-chan struct{} {
	if countable(p) {
		pc.mu.Lock()
		defer pc.mu.Unlock()
		if c := pc.calls[p]; c != nil {
			return c.idle
		}
	}
	done := make(chan struct{})
	close(done)
	return done
}

// countable reports whether p can be a map key.
func countable(p providers.LLMProvider) bool {
	return p != nil && reflect.TypeOf(p).Comparable()
}
