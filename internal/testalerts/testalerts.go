// ClawEh
// License: MIT

// Package testalerts captures alerts raised through the process default in
// tests: Install swaps in a recorder for the test's lifetime.
package testalerts

import (
	"context"
	"sync"
	"testing"

	"github.com/tenebris-tech/alerter"

	"github.com/PivotLLM/ClawEh/alerts"
)

// Recorder keeps every alert it is sent.
type Recorder struct {
	mu     sync.Mutex
	alerts []alerter.Alert
}

func (r *Recorder) Send(a alerter.Alert) {
	r.mu.Lock()
	r.alerts = append(r.alerts, a)
	r.mu.Unlock()
}
func (r *Recorder) Normal(string, string, ...string)    {}
func (r *Recorder) Urgent(string, string, ...string)    {}
func (r *Recorder) Emergency(string, string, ...string) {}
func (r *Recorder) Close(context.Context) error         { return nil }

// Alerts returns a copy of what was recorded.
func (r *Recorder) Alerts() []alerter.Alert {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]alerter.Alert(nil), r.alerts...)
}

// Install makes the process alerter a Recorder until the test ends. Tests
// that use it must not run in parallel.
func Install(t *testing.T) *Recorder {
	t.Helper()
	r := &Recorder{}
	alerts.Set(r)
	t.Cleanup(func() { alerts.Set(nil) })
	return r
}
