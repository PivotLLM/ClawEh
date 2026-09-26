package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/internal/admin"
	"github.com/PivotLLM/ClawEh/internal/testalerts"
	"github.com/PivotLLM/ClawEh/web/backend/middleware"
)

const (
	authTestUser = "alice"
	authTestPass = "a perfectly fine password"
)

type authEnv struct {
	t     *testing.T
	h     *Handler
	mux   *http.ServeMux
	store *middleware.AuthStore
	rec   *testalerts.Recorder
	now   time.Time
}

// newAuthEnv builds a handler with a credentials file (or none) and a fixed,
// advanceable clock shared by the session store and the login limiter.
func newAuthEnv(t *testing.T, withCreds bool) *authEnv {
	t.Helper()
	path := admin.Path(t.TempDir())
	if withCreds {
		if err := admin.Write(path, authTestUser, authTestPass); err != nil {
			t.Fatal(err)
		}
	}
	env := &authEnv{t: t, now: time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)}
	clock := func() time.Time { return env.now }
	env.store = middleware.NewAuthStore(path, middleware.WithAuthClock(clock))
	env.h = NewHandler(setupTestEnv(t))
	env.h.loginLimiter = middleware.NewLoginLimiter(clock)
	env.h.SetAuth(env.store)
	env.rec = &testalerts.Recorder{}
	env.h.SetAlerter(env.rec)
	env.mux = http.NewServeMux()
	env.h.registerAuthRoutes(env.mux)
	return env
}

func (e *authEnv) login(user, pass, ip string) *httptest.ResponseRecorder {
	e.t.Helper()
	body, err := json.Marshal(map[string]string{"username": user, "password": pass})
	if err != nil {
		e.t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(string(body)))
	req.RemoteAddr = ip + ":50000"
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	return rec
}

func (e *authEnv) status(cookie *http.Cookie) map[string]any {
	e.t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/auth/status", nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		e.t.Fatalf("status body %q: %v", rec.Body.String(), err)
	}
	return out
}

func TestAuthStatus_NoAdminAccount(t *testing.T) {
	env := newAuthEnv(t, false)
	st := env.status(nil)
	if st["configured"] != false || st["authenticated"] != false {
		t.Fatalf("status = %v", st)
	}
	rec := env.login(authTestUser, authTestPass, "127.0.0.1")
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "run: claw admin") {
		t.Fatalf("login with no account: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAuthStatus_HandlerWithoutStore(t *testing.T) {
	h := NewHandler(setupTestEnv(t))
	mux := http.NewServeMux()
	h.registerAuthRoutes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/auth/status", nil))
	if !strings.Contains(rec.Body.String(), `"configured":false`) {
		t.Fatalf("status without a store = %s", rec.Body.String())
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{}`)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("login without a store = %d", rec.Code)
	}
}

func TestAuthLogin_SuccessSetsCookieAndStatus(t *testing.T) {
	env := newAuthEnv(t, true)
	rec := env.login(authTestUser, authTestPass, "127.0.0.1")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("login = %d %s", rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != middleware.SessionCookieInsecure || cookies[0].Value == "" {
		t.Fatalf("cookies = %v", cookies)
	}
	c := cookies[0]
	if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Secure {
		t.Fatalf("cookie attributes over HTTP = %+v", c)
	}

	st := env.status(c)
	if st["configured"] != true || st["authenticated"] != true || st["username"] != authTestUser {
		t.Fatalf("status with session = %v", st)
	}
	if st := env.status(nil); st["authenticated"] != false || st["configured"] != true {
		t.Fatalf("status without session = %v", st)
	}

	// Logout ends the session and clears the cookie.
	req := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	req.AddCookie(c)
	out := httptest.NewRecorder()
	env.mux.ServeHTTP(out, req)
	if out.Code != http.StatusNoContent {
		t.Fatalf("logout = %d", out.Code)
	}
	cleared := false
	for _, cc := range out.Result().Cookies() {
		if cc.Name == middleware.SessionCookieInsecure && cc.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("logout did not clear the session cookie")
	}
	if st := env.status(c); st["authenticated"] != false {
		t.Fatalf("session still valid after logout: %v", st)
	}
}

func TestAuthLogin_WrongCredentialsAreOneGenericError(t *testing.T) {
	env := newAuthEnv(t, true)
	wrongPass := env.login(authTestUser, "not the password at all", "127.0.0.1")
	wrongUser := env.login("mallory", authTestPass, "127.0.0.1")
	for _, rec := range []*httptest.ResponseRecorder{wrongPass, wrongUser} {
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
		if len(rec.Result().Cookies()) != 0 {
			t.Fatal("a failed login set a cookie")
		}
	}
	if wrongPass.Body.String() != wrongUser.Body.String() {
		t.Fatalf("bodies differ: %q vs %q", wrongPass.Body.String(), wrongUser.Body.String())
	}
	if !strings.Contains(wrongPass.Body.String(), "invalid username or password") {
		t.Fatalf("body = %s", wrongPass.Body.String())
	}
	if len(env.rec.Alerts()) != 0 {
		t.Fatal("two failures raised an alert")
	}
}

func TestAuthLogin_BadBody(t *testing.T) {
	env := newAuthEnv(t, true)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader("not json"))
	req.RemoteAddr = "127.0.0.1:1"
	rec := httptest.NewRecorder()
	env.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestAuthLogin_LockoutAfterFiveFailuresAlertsOnce(t *testing.T) {
	env := newAuthEnv(t, true)
	const ip = "203.0.113.7"
	for i := range middleware.LoginFailuresPerIP {
		if rec := env.login(authTestUser, "wrong password number x", ip); rec.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d: status = %d", i+1, rec.Code)
		}
	}
	alerts := env.rec.Alerts()
	if len(alerts) != 1 || alerts[0].EventID != "auth-lockout" || !strings.Contains(alerts[0].Description, ip) {
		t.Fatalf("alerts after lockout = %+v", alerts)
	}

	// Locked: even the right password is refused with 429 and a Retry-After.
	rec := env.login(authTestUser, authTestPass, ip)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("locked login = %d", rec.Code)
	}
	if ra := rec.Header().Get("Retry-After"); ra != "60" {
		t.Fatalf("Retry-After = %q, want 60", ra)
	}
	// Another address is unaffected.
	if other := env.login(authTestUser, authTestPass, "127.0.0.1"); other.Code != http.StatusNoContent {
		t.Fatalf("other address login = %d", other.Code)
	}

	// After the lock, five more failures lock for twice as long and do not
	// alert again.
	env.now = env.now.Add(middleware.LoginLockBase + time.Second)
	for range middleware.LoginFailuresPerIP {
		env.login(authTestUser, "still wrong password", ip)
	}
	if len(env.rec.Alerts()) != 1 {
		t.Fatalf("second lockout alerted again: %d alerts", len(env.rec.Alerts()))
	}
	rec = env.login(authTestUser, authTestPass, ip)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "120" {
		t.Fatalf("second lock: %d Retry-After=%s", rec.Code, rec.Header().Get("Retry-After"))
	}
	env.now = env.now.Add(2*middleware.LoginLockBase + time.Second)
	if rec := env.login(authTestUser, authTestPass, ip); rec.Code != http.StatusNoContent {
		t.Fatalf("login after lock expiry = %d", rec.Code)
	}
}
