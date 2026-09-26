package middleware

import (
	"bytes"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/internal/admin"
	"github.com/PivotLLM/ClawEh/internal/audit"
	"github.com/PivotLLM/ClawEh/logger"
)

const (
	testUser = "alice"
	testPass = "a perfectly fine password"
)

// writeCreds writes a valid credentials file in a fresh dir and returns its path.
func writeCreds(t *testing.T) string {
	t.Helper()
	path := admin.Path(t.TempDir())
	if err := admin.Write(path, testUser, testPass); err != nil {
		t.Fatal(err)
	}
	return path
}

// testClock is a settable clock for the store and limiter.
type testClock struct{ t time.Time }

func (c *testClock) now() time.Time { return c.t }
func (c *testClock) advance(d time.Duration) {
	c.t = c.t.Add(d)
}

func newClock() *testClock {
	return &testClock{t: time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)}
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
}

// authHandler wires the middleware the way httpHost does, with a fixed store
// and exempt set.
func authHandler(store *AuthStore, exempt *AuthExempt) http.Handler {
	return Auth(func() *AuthStore { return store }, func() *AuthExempt { return exempt }, okHandler())
}

func serve(h http.Handler, method, path string, cookie *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = "127.0.0.1:4321"
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAuthStore_NoFileIsUnconfigured(t *testing.T) {
	s := NewAuthStore(admin.Path(t.TempDir()))
	if s.Configured() {
		t.Fatal("Configured() = true with no credentials file")
	}
	if _, ok := s.Login(testUser, testPass); ok {
		t.Fatal("Login succeeded with no credentials file")
	}

	rec := serve(authHandler(s, nil), http.MethodGet, "/api/config", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, `"no admin account"`) || !strings.Contains(body, `"run: claw admin"`) {
		t.Fatalf("body = %s", body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q", ct)
	}
}

func TestAuthStore_LoosePermissionsIgnoredWithFix(t *testing.T) {
	path := writeCreds(t)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	restore := logger.RedirectForTest(&buf)
	s := NewAuthStore(path)
	restore()

	if s.Configured() {
		t.Fatal("a world-readable credentials file was accepted")
	}
	if !strings.Contains(buf.String(), `"level":"error"`) || !strings.Contains(buf.String(), "chmod 600 "+path) {
		t.Fatalf("expected an error log naming the chmod fix, got: %s", buf.String())
	}

	// Fixing the mode and reloading makes it usable without a restart.
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	s.Reload()
	if !s.Configured() {
		t.Fatal("Reload after chmod 600 did not pick the file up")
	}
}

func TestAuthStore_LoginAndValidate(t *testing.T) {
	s := NewAuthStore(writeCreds(t))
	if !s.Configured() {
		t.Fatal("Configured() = false")
	}
	for _, tc := range []struct{ user, pass string }{
		{testUser, "wrong password entirely"},
		{"bob", testPass},
		{"", ""},
		{"Alice", testPass},
	} {
		if _, ok := s.Login(tc.user, tc.pass); ok {
			t.Errorf("Login(%q, %q) succeeded", tc.user, tc.pass)
		}
	}
	id, ok := s.Login(testUser, testPass)
	if !ok || id == "" {
		t.Fatal("Login with the right credentials failed")
	}
	if user, ok := s.Validate(id); !ok || user != testUser {
		t.Fatalf("Validate = %q, %v", user, ok)
	}
	if _, ok := s.Validate(id + "x"); ok {
		t.Fatal("a tampered session id validated")
	}
	if _, ok := s.Validate(""); ok {
		t.Fatal("an empty session id validated")
	}
	s.Logout(id)
	if _, ok := s.Validate(id); ok {
		t.Fatal("session validated after Logout")
	}
	if s.SessionCount() != 0 {
		t.Fatalf("SessionCount = %d after logout", s.SessionCount())
	}
}

func TestAuthStore_IdleAndAbsoluteExpiry(t *testing.T) {
	clock := newClock()
	s := NewAuthStore(writeCreds(t), WithAuthClock(clock.now))

	// Idle: silence past the idle limit ends the session, but use slides it.
	id, _ := s.Login(testUser, testPass)
	for range 3 {
		clock.advance(DefaultSessionIdle - time.Minute)
		if _, ok := s.Validate(id); !ok {
			t.Fatal("session expired while in use inside the idle window")
		}
	}
	clock.advance(DefaultSessionIdle + time.Second)
	if _, ok := s.Validate(id); ok {
		t.Fatal("session survived past the idle timeout")
	}

	// Absolute: constant use does not extend past the maximum lifetime.
	id, _ = s.Login(testUser, testPass)
	start := clock.t
	for clock.t.Sub(start) < DefaultSessionMax-time.Hour {
		clock.advance(time.Hour)
		if _, ok := s.Validate(id); !ok {
			t.Fatalf("session expired at %v while in constant use", clock.t.Sub(start))
		}
	}
	clock.advance(2 * time.Hour)
	if _, ok := s.Validate(id); ok {
		t.Fatal("session survived past the absolute lifetime")
	}

	// Expired sessions are pruned on the next login rather than kept forever.
	s.Login(testUser, testPass)
	clock.advance(DefaultSessionMax + time.Hour)
	s.Login(testUser, testPass)
	if n := s.SessionCount(); n != 1 {
		t.Fatalf("SessionCount = %d, want 1 (expired sessions pruned on login)", n)
	}
}

func TestAuthStore_ReloadRevokesOnChange(t *testing.T) {
	path := writeCreds(t)
	s := NewAuthStore(path)
	id, _ := s.Login(testUser, testPass)

	// Same content: sessions survive.
	s.Reload()
	if _, ok := s.Validate(id); !ok {
		t.Fatal("Reload with unchanged credentials revoked the session")
	}

	// New password: everyone is signed out and only the new password works.
	if err := admin.Write(path, testUser, "a different fine password"); err != nil {
		t.Fatal(err)
	}
	s.Reload()
	if _, ok := s.Validate(id); ok {
		t.Fatal("session survived a credentials change")
	}
	if _, ok := s.Login(testUser, testPass); ok {
		t.Fatal("old password still accepted after reload")
	}
	if _, ok := s.Login(testUser, "a different fine password"); !ok {
		t.Fatal("new password rejected after reload")
	}

	// File removed: unconfigured again, sessions gone.
	id, _ = s.Login(testUser, "a different fine password")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	s.Reload()
	if s.Configured() {
		t.Fatal("Configured() = true after the file was removed")
	}
	if _, ok := s.Validate(id); ok {
		t.Fatal("session survived the credentials file being removed")
	}
}

func TestAuthStore_WatchReloadsOnFileChange(t *testing.T) {
	path := writeCreds(t)
	s := NewAuthStore(path)
	id, _ := s.Login(testUser, testPass)

	go s.Watch(t.Context(), 5*time.Millisecond)

	if err := admin.Write(path, testUser, "a different fine password"); err != nil {
		t.Fatal(err)
	}
	// mtime resolution can be coarse; make the change unmistakable.
	if err := os.Chtimes(path, time.Now(), time.Now().Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := s.Validate(id); !ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Watch did not reload after the credentials file changed")
}

func TestAuth_ExemptPathsPassWithoutSession(t *testing.T) {
	s := NewAuthStore(admin.Path(t.TempDir())) // no account at all: the strictest state
	exempt := CompileAuthExempt("/webhook/line")
	h := authHandler(s, exempt)

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/health"},
		{http.MethodGet, "/ready"},
		{http.MethodGet, "/ping"},
		{http.MethodPost, "/api/v1/oauth/token"},
		{http.MethodGet, "/api/v1/authorize"},
		{http.MethodPost, "/api/message/abcdef"},
		{http.MethodPost, "/api/auth/login"},
		{http.MethodPost, "/api/auth/logout"},
		{http.MethodGet, "/api/auth/status"},
		{http.MethodPost, "/webhook/line"},
		// The SPA shell and assets: no secrets, and where the login page lives.
		{http.MethodGet, "/"},
		{http.MethodGet, "/login"},
		{http.MethodGet, "/assets/index-abc123.js"},
		{http.MethodHead, "/logo.png"},
	} {
		if rec := serve(h, tc.method, tc.path, nil); rec.Code != http.StatusOK {
			t.Errorf("%s %s = %d, want 200 (exempt)", tc.method, tc.path, rec.Code)
		}
	}

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/config"},
		{http.MethodGet, "/api"},
		{http.MethodPut, "/api/config"},
		{http.MethodGet, "/api/message/abcdef"}, // only POST is the token API
		{http.MethodGet, "/api/auth/login"},     // only POST is exempt
		{http.MethodGet, "/webui/ws"},
		{http.MethodPost, "/"},
		{http.MethodPost, "/webhook/slack"},
		{http.MethodGet, "/api/v1x"},
	} {
		if rec := serve(h, tc.method, tc.path, nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s = %d, want 401", tc.method, tc.path, rec.Code)
		}
	}

	// A nil exempt set (before ApplyPolicy) still exempts the built-in list
	// and nothing else.
	h = authHandler(s, nil)
	if rec := serve(h, http.MethodGet, "/health", nil); rec.Code != http.StatusOK {
		t.Errorf("nil exempt: /health = %d", rec.Code)
	}
	if rec := serve(h, http.MethodPost, "/webhook/line", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("nil exempt: /webhook/line = %d, want 401", rec.Code)
	}
}

func TestAuth_NilStoreFailsClosed(t *testing.T) {
	h := Auth(func() *AuthStore { return nil }, func() *AuthExempt { return nil }, okHandler())
	if rec := serve(h, http.MethodGet, "/api/config", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if rec := serve(h, http.MethodGet, "/health", nil); rec.Code != http.StatusOK {
		t.Fatalf("/health = %d, want 200", rec.Code)
	}
}

func TestAuth_SessionCookieAdmitsEitherName(t *testing.T) {
	s := NewAuthStore(writeCreds(t))
	h := authHandler(s, nil)
	id, _ := s.Login(testUser, testPass)

	for _, name := range []string{SessionCookieSecure, SessionCookieInsecure} {
		rec := serve(h, http.MethodGet, "/api/config", &http.Cookie{Name: name, Value: id})
		if rec.Code != http.StatusOK {
			t.Errorf("cookie %s: status = %d, want 200", name, rec.Code)
		}
		rec = serve(h, http.MethodGet, "/webui/ws", &http.Cookie{Name: name, Value: id})
		if rec.Code != http.StatusOK {
			t.Errorf("cookie %s on /webui/ws: status = %d, want 200", name, rec.Code)
		}
	}
	rec := serve(h, http.MethodGet, "/api/config", &http.Cookie{Name: SessionCookieInsecure, Value: "forged"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("forged cookie: status = %d, want 401", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "authentication required") || strings.Contains(body, "claw admin") {
		t.Fatalf("configured install must not hint at claw admin: %s", body)
	}
}

func TestAuth_SetsAuditActor(t *testing.T) {
	s := NewAuthStore(writeCreds(t))
	id, _ := s.Login(testUser, testPass)
	var actor string
	h := Auth(func() *AuthStore { return s }, func() *AuthExempt { return nil },
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			actor = audit.ActorFromContext(r.Context())
		}))
	serve(h, http.MethodPut, "/api/config", &http.Cookie{Name: SessionCookieInsecure, Value: id})
	if actor != testUser {
		t.Fatalf("actor = %q, want %q", actor, testUser)
	}
	actor = "unset"
	serve(h, http.MethodGet, "/health", nil)
	if actor != "" {
		t.Fatalf("exempt request carried actor %q", actor)
	}
}

func TestSessionCookie_NameAndSecureFollowTransport(t *testing.T) {
	plain := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	rec := httptest.NewRecorder()
	SetSessionCookie(rec, plain, "sid")
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %v", cookies)
	}
	c := cookies[0]
	if c.Name != SessionCookieInsecure || c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" {
		t.Fatalf("HTTP cookie = %+v", c)
	}

	secure := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	secure.TLS = &tls.ConnectionState{}
	rec = httptest.NewRecorder()
	SetSessionCookie(rec, secure, "sid")
	c = rec.Result().Cookies()[0]
	if c.Name != SessionCookieSecure || !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" {
		t.Fatalf("TLS cookie = %+v", c)
	}

	rec = httptest.NewRecorder()
	ClearSessionCookie(rec, plain)
	cleared := rec.Result().Cookies()
	if len(cleared) != 2 {
		t.Fatalf("ClearSessionCookie set %d cookies, want both names", len(cleared))
	}
	for _, c := range cleared {
		if c.MaxAge >= 0 {
			t.Errorf("cleared cookie %s has MaxAge %d", c.Name, c.MaxAge)
		}
	}
}

func TestLoginLimiter_PerIPBackoffDoublesAndCaps(t *testing.T) {
	clock := newClock()
	l := NewLoginLimiter(clock.now)
	const ip = "203.0.113.9"

	for i := 1; i < LoginFailuresPerIP; i++ {
		if locked, first := l.Failure(ip); locked != 0 || first {
			t.Fatalf("failure %d locked = %v, first = %v", i, locked, first)
		}
		if wait := l.Check(ip); wait != 0 {
			t.Fatalf("locked after %d failures", i)
		}
	}
	locked, first := l.Failure(ip)
	if locked != LoginLockBase || !first {
		t.Fatalf("5th failure: locked = %v, first = %v; want 1m, true", locked, first)
	}
	if wait := l.Check(ip); wait != LoginLockBase {
		t.Fatalf("Check during lock = %v, want %v", wait, LoginLockBase)
	}
	if wait := l.Check("198.51.100.1"); wait != 0 {
		t.Fatal("another address was locked by this one's failures")
	}
	clock.advance(LoginLockBase + time.Second)
	if wait := l.Check(ip); wait != 0 {
		t.Fatalf("still locked after the lock expired: %v", wait)
	}

	// Each further lockout doubles, alerts only once, and caps at an hour.
	want := LoginLockBase
	for round := 2; round <= 8; round++ {
		want = min(want*2, LoginLockMax)
		var got time.Duration
		for range LoginFailuresPerIP {
			got, first = l.Failure(ip)
		}
		if got != want || first {
			t.Fatalf("lockout %d = %v, first = %v; want %v, false", round, got, first, want)
		}
		clock.advance(got + time.Second)
	}

	// A successful login resets the escalation.
	l.Success(ip)
	for range LoginFailuresPerIP {
		locked, _ = l.Failure(ip)
	}
	if locked != LoginLockBase {
		t.Fatalf("after Success the next lockout = %v, want %v", locked, LoginLockBase)
	}
}

func TestLoginLimiter_WindowExpiryForgetsFailures(t *testing.T) {
	clock := newClock()
	l := NewLoginLimiter(clock.now)
	const ip = "203.0.113.9"
	for range LoginFailuresPerIP - 1 {
		l.Failure(ip)
	}
	clock.advance(LoginFailureWindow + time.Second)
	if locked, _ := l.Failure(ip); locked != 0 {
		t.Fatalf("a failure after the window locked the address: %v", locked)
	}
}

func TestLoginLimiter_GlobalCap(t *testing.T) {
	clock := newClock()
	l := NewLoginLimiter(clock.now)
	// Spread failures across addresses so no single one trips its own limit.
	for i := range LoginGlobalFailures - 1 {
		ip := "10.0." + string(rune('a'+i%26)) + "." + string(rune('a'+i/26))
		if locked, _ := l.Failure(ip); locked != 0 {
			t.Fatalf("failure %d locked: %v", i, locked)
		}
	}
	if locked, _ := l.Failure("10.9.9.9"); locked != LoginGlobalLock {
		t.Fatalf("100th failure locked = %v, want %v", locked, LoginGlobalLock)
	}
	if wait := l.Check("192.0.2.1"); wait != LoginGlobalLock {
		t.Fatalf("an uninvolved address is not globally locked: %v", wait)
	}
	clock.advance(LoginGlobalLock + time.Second)
	if wait := l.Check("192.0.2.1"); wait != 0 {
		t.Fatalf("global lock did not expire: %v", wait)
	}
}

func TestCompileAuthExempt_IgnoresBlankPatterns(t *testing.T) {
	e := CompileAuthExempt("", "  ", "/webhook/line")
	r := httptest.NewRequest(http.MethodPost, "/webhook/line", nil)
	if !e.Matches(r) {
		t.Fatal("extra pattern not exempt")
	}
	if e.Matches(httptest.NewRequest(http.MethodGet, filepath.Join("/", "api", "config"), nil)) {
		t.Fatal("/api/config exempt")
	}
}
