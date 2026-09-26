package middleware

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/PivotLLM/ClawEh/internal/admin"
	"github.com/PivotLLM/ClawEh/logger"
)

// Session cookie names. The __Host- prefix makes the browser refuse the cookie
// unless it is Secure, has Path=/ and no Domain, which pins it to this exact
// origin — but a browser also refuses to store a __Host- cookie set over plain
// HTTP, so a loopback HTTP install gets the plain name instead. The middleware
// accepts either; the request's own transport (r.TLS) decides which is set.
const (
	SessionCookieSecure   = "__Host-claw_session"
	SessionCookieInsecure = "claw_session"
)

// Session lifetimes: a session ends after DefaultSessionIdle without a
// request, and DefaultSessionMax after login regardless of use.
const (
	DefaultSessionIdle = 12 * time.Hour
	DefaultSessionMax  = 7 * 24 * time.Hour
)

// CredentialsPollInterval is how often the gateway re-stats the credentials
// file to notice `claw admin` having run.
const CredentialsPollInterval = 60 * time.Second

type authSession struct {
	username string
	created  time.Time
	lastSeen time.Time
}

// AuthStore holds the admin credentials loaded from <CLAW_HOME>/credentials.json
// and the server-side login sessions. Sessions live in memory only: a gateway
// restart signs everyone out. The store outlives config reloads (it belongs to
// the listener, like the IP allowlist), so a reload does not.
type AuthStore struct {
	path     string
	now      func() time.Time
	idle     time.Duration
	absolute time.Duration

	mu    sync.RWMutex
	creds *admin.Credentials // nil when no usable credentials file
	// fileSeen is the (mtime, size) of the credentials file at the last Reload,
	// which is what Watch compares against; zero when the file was absent.
	fileSeen fileStamp
	sessions map[[sha256.Size]byte]*authSession
}

type fileStamp struct {
	exists  bool
	modTime time.Time
	size    int64
}

// AuthOption configures NewAuthStore.
type AuthOption func(*AuthStore)

// WithAuthClock replaces the store's clock (tests drive expiry with it).
func WithAuthClock(now func() time.Time) AuthOption {
	return func(s *AuthStore) { s.now = now }
}

// WithSessionLifetimes overrides the idle and absolute session lifetimes.
func WithSessionLifetimes(idle, absolute time.Duration) AuthOption {
	return func(s *AuthStore) {
		s.idle = idle
		s.absolute = absolute
	}
}

// NewAuthStore creates the store for the credentials file at credentialsPath
// and loads it once. A missing or unusable file leaves the store unconfigured
// (every login fails, every protected request gets 401) and is logged.
func NewAuthStore(credentialsPath string, opts ...AuthOption) *AuthStore {
	s := &AuthStore{
		path:     credentialsPath,
		now:      time.Now,
		idle:     DefaultSessionIdle,
		absolute: DefaultSessionMax,
		sessions: make(map[[sha256.Size]byte]*authSession),
	}
	for _, opt := range opts {
		opt(s)
	}
	s.Reload()
	return s
}

// Path returns the credentials file the store reads.
func (s *AuthStore) Path() string { return s.path }

// Configured reports whether a usable admin account is loaded.
func (s *AuthStore) Configured() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.creds != nil
}

// Reload re-reads the credentials file. When the account changed — created,
// replaced, removed, or made unusable — every session is revoked, so a new
// password takes effect at once. The credentials poller calls this on a file
// change; anything else that wants a re-read (the TLS watcher) may call it too.
func (s *AuthStore) Reload() {
	creds, err := admin.Load(s.path)
	stamp := statFile(s.path)

	s.mu.Lock()
	changed := !sameCredentials(s.creds, creds)
	s.creds = creds
	s.fileSeen = stamp
	if changed {
		s.sessions = make(map[[sha256.Size]byte]*authSession)
	}
	s.mu.Unlock()

	var perr *admin.PermissionError
	switch {
	case err == nil:
		if changed {
			logger.InfoCF("auth", "Admin credentials loaded; all sessions signed out", map[string]any{
				"path": s.path, "username": creds.Username,
			})
		}
	case errors.Is(err, admin.ErrNotConfigured):
		logger.WarnCF("auth", "No admin account: the WebUI and API are locked until `claw admin` is run", map[string]any{
			"path": s.path,
		})
	case errors.As(err, &perr):
		logger.ErrorCF("auth", "Credentials file ignored: other accounts can read it", map[string]any{
			"path": s.path, "fix": perr.Fix(),
		})
	default:
		logger.ErrorCF("auth", "Credentials file ignored", map[string]any{
			"path": s.path, "error": err.Error(),
		})
	}
}

// Watch polls the credentials file's mtime and size every interval and calls
// Reload when they change. It returns when ctx is done.
func (s *AuthStore) Watch(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if s.fileChanged() {
				s.Reload()
			}
		}
	}
}

func (s *AuthStore) fileChanged() bool {
	now := statFile(s.path)
	s.mu.RLock()
	defer s.mu.RUnlock()
	return now != s.fileSeen
}

func statFile(path string) fileStamp {
	fi, err := os.Stat(path)
	if err != nil {
		return fileStamp{}
	}
	return fileStamp{exists: true, modTime: fi.ModTime(), size: fi.Size()}
}

func sameCredentials(a, b *admin.Credentials) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Username == b.Username && a.PasswordHash == b.PasswordHash
}

// Login verifies the credentials and, on success, opens a session and returns
// its id (the cookie value). It fails for any reason — no account, wrong
// username, wrong password — with the same false, so the caller has one error
// to report.
func (s *AuthStore) Login(username, password string) (string, bool) {
	s.mu.RLock()
	creds := s.creds
	s.mu.RUnlock()
	if creds == nil || !creds.Verify(username, password) {
		return "", false
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		logger.ErrorCF("auth", "Session id generation failed", map[string]any{"error": err.Error()})
		return "", false
	}
	id := base64.RawURLEncoding.EncodeToString(raw)
	now := s.now()

	s.mu.Lock()
	s.pruneLocked(now)
	s.sessions[sessionKey(id)] = &authSession{username: creds.Username, created: now, lastSeen: now}
	s.mu.Unlock()
	return id, true
}

// Logout deletes the session; an unknown id is a no-op.
func (s *AuthStore) Logout(id string) {
	s.mu.Lock()
	delete(s.sessions, sessionKey(id))
	s.mu.Unlock()
}

// Validate reports whether id is a live session and slides its idle timer.
// An expired session is removed on the way out.
func (s *AuthStore) Validate(id string) (string, bool) {
	if id == "" {
		return "", false
	}
	key := sessionKey(id)
	now := s.now()

	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[key]
	if !ok {
		return "", false
	}
	if s.expired(sess, now) {
		delete(s.sessions, key)
		return "", false
	}
	sess.lastSeen = now
	return sess.username, true
}

// RevokeAll signs every session out.
func (s *AuthStore) RevokeAll() {
	s.mu.Lock()
	s.sessions = make(map[[sha256.Size]byte]*authSession)
	s.mu.Unlock()
}

// SessionCount returns the number of stored sessions, expired ones included
// until they are next touched or pruned.
func (s *AuthStore) SessionCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.sessions)
}

func (s *AuthStore) expired(sess *authSession, now time.Time) bool {
	return now.Sub(sess.lastSeen) > s.idle || now.Sub(sess.created) > s.absolute
}

// pruneLocked drops expired sessions. Called with mu held, on login, so the
// map is bounded by live sessions plus whatever expired since the last login.
func (s *AuthStore) pruneLocked(now time.Time) {
	for k, sess := range s.sessions {
		if s.expired(sess, now) {
			delete(s.sessions, k)
		}
	}
}

func sessionKey(id string) [sha256.Size]byte {
	return sha256.Sum256([]byte(id))
}

// Authenticate finds a live session in the request's cookies (either cookie
// name) and returns its username.
func (s *AuthStore) Authenticate(r *http.Request) (string, bool) {
	for _, name := range []string{SessionCookieSecure, SessionCookieInsecure} {
		c, err := r.Cookie(name)
		if err != nil {
			continue
		}
		if user, ok := s.Validate(c.Value); ok {
			return user, true
		}
	}
	return "", false
}

// HasSession is Authenticate as a predicate, in the shape the WebUI channel
// takes for its WebSocket handshake check.
func (s *AuthStore) HasSession(r *http.Request) bool {
	_, ok := s.Authenticate(r)
	return ok
}

// SessionCookieName picks the cookie for the transport the request arrived
// on: the __Host- Secure cookie over TLS, the plain one over HTTP.
func SessionCookieName(r *http.Request) string {
	if r.TLS != nil {
		return SessionCookieSecure
	}
	return SessionCookieInsecure
}

// SetSessionCookie writes the session cookie for id on the response.
func SetSessionCookie(w http.ResponseWriter, r *http.Request, id string) {
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // Secure follows the transport: a Secure cookie set over the plain-HTTP loopback listener would be refused by the browser (see SessionCookieInsecure)
		Name:     SessionCookieName(r),
		Value:    id,
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteStrictMode,
	})
}

// ClearSessionCookie expires both session cookies on the response.
func ClearSessionCookie(w http.ResponseWriter, r *http.Request) {
	for _, name := range []string{SessionCookieSecure, SessionCookieInsecure} {
		http.SetCookie(w, &http.Cookie{ //nolint:gosec // Secure follows the transport, as in SetSessionCookie; the expiry must reach the same cookie the login set
			Name:     name,
			Value:    "",
			Path:     "/",
			HttpOnly: true,
			Secure:   name == SessionCookieSecure || r.TLS != nil,
			SameSite: http.SameSiteStrictMode,
			MaxAge:   -1,
		})
	}
}
