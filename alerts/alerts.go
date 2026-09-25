// ClawEh
// License: MIT

// Package alerts holds the process-wide operator alerter for code that has no
// owner to be handed one: package-level singletons, free functions and
// providers rebuilt per tool registry. Anything the gateway or the agent loop
// constructs directly keeps an explicit alerter instead (see ALERTS.md).
package alerts

import (
	"sync"

	"github.com/tenebris-tech/alerter"
)

var (
	mu      sync.RWMutex
	current alerter.Alerter = alerter.Nop{}
)

// Set installs the process alerter. The gateway calls it once at startup;
// nil restores the no-op default (tests use that in a Cleanup).
func Set(a alerter.Alerter) {
	if a == nil {
		a = alerter.Nop{}
	}
	mu.Lock()
	current = a
	mu.Unlock()
}

// Default returns the process alerter, never nil.
func Default() alerter.Alerter {
	mu.RLock()
	defer mu.RUnlock()
	return current
}

// Send raises an alert through the process alerter.
func Send(a alerter.Alert) {
	Default().Send(a)
}
