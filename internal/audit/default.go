// ClawEh
// License: MIT

package audit

import (
	"context"
	"path/filepath"
	"sync"
)

// actorKey is the type of ActorKey; a private type keeps the key from
// colliding with any other package's context values.
type actorKey struct{}

// ActorKey is the context key under which the authenticated WebUI username is
// stored. The auth middleware sets it (context.WithValue(ctx, audit.ActorKey,
// username) or WithActor); ActorFromContext reads it when an HTTP handler
// records an event.
var ActorKey any = actorKey{}

// WithActor returns ctx carrying the authenticated username.
func WithActor(ctx context.Context, username string) context.Context {
	return context.WithValue(ctx, ActorKey, username)
}

// ActorFromContext returns the authenticated username stored under ActorKey,
// or "" when the request was not authenticated.
func ActorFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if s, ok := ctx.Value(ActorKey).(string); ok {
		return s
	}
	return ""
}

var (
	defaultMu    sync.RWMutex
	defaultStore *Store
)

// Init opens <dataDir>/audit.db and installs it as the process-wide store that
// Default returns. Calling it again replaces (and closes) the previous store.
func Init(dataDir string) error {
	s, err := Open(context.Background(), filepath.Join(dataDir, FileName))
	if err != nil {
		return err
	}
	defaultMu.Lock()
	prev := defaultStore
	defaultStore = s
	defaultMu.Unlock()
	return prev.Close()
}

// Default returns the process-wide store, or nil before Init. Every Store
// method is nil-safe, so callers may use the result without checking.
func Default() *Store {
	defaultMu.RLock()
	defer defaultMu.RUnlock()
	return defaultStore
}

// Close flushes and closes the process-wide store; a no-op before Init.
func Close() error {
	defaultMu.Lock()
	s := defaultStore
	defaultStore = nil
	defaultMu.Unlock()
	return s.Close()
}

// Auth records an authentication event on the default store. kind is what
// happened ("login", "logout", "lockout"), username the account it concerns,
// ip the client address and outcome OutcomeOK or OutcomeError (a failed login
// is an error outcome on a "login" event).
func Auth(kind, username, ip, outcome string) {
	Default().Record(Event{
		Kind:    KindAuth,
		Actor:   username,
		Sender:  ip,
		Summary: kind,
		Outcome: outcome,
	})
}
