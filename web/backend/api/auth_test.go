package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/tenebris-tech/alerter"

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

// assertLocked checks a locked answer: 429, Retry-After, and the one generic
// body every lock gives, whatever is locked and whether the account exists.
func assertLocked(t *testing.T, rec *httptest.ResponseRecorder, retryAfter string) {
	t.Helper()
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 (%s)", rec.Code, rec.Body.String())
	}
	if ra := rec.Header().Get("Retry-After"); ra != retryAfter {
		t.Fatalf("Retry-After = %q, want %q", ra, retryAfter)
	}
	want := `{"error":"too many failed logins","retry_after":` + retryAfter + "}\n"
	if rec.Body.String() != want {
		t.Fatalf("locked body = %q, want %q", rec.Body.String(), want)
	}
}

func alertIDs(rec *testalerts.Recorder) []string {
	alerts := rec.Alerts()
	ids := make([]string, 0, len(alerts))
	for _, a := range alerts {
		ids = append(ids, a.EventID)
	}
	return ids
}

func TestAuthLogin_IPLockout(t *testing.T) {
	env := newAuthEnv(t, true)
	const ip = "203.0.113.7"
	// Different usernames so only the address threshold is reached.
	for i := range middleware.LoginIPFailures - 1 {
		if rec := env.login(fmt.Sprintf("guess%d", i), "wrong password", ip); rec.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d: status = %d", i+1, rec.Code)
		}
	}
	if len(env.rec.Alerts()) != 0 {
		t.Fatal("alert before the threshold")
	}
	if rec := env.login("guess-last", "wrong password", ip); rec.Code != http.StatusUnauthorized {
		t.Fatalf("locking failure: status = %d", rec.Code)
	}
	alerts := env.rec.Alerts()
	if len(alerts) != 1 || alerts[0].EventID != "auth-lockout-ip" || alerts[0].Priority != alerter.Normal ||
		!strings.Contains(alerts[0].Description, ip) {
		t.Fatalf("alerts after lockout = %+v", alerts)
	}

	// Locked: even the right password is refused, and every attempt restarts
	// the five minutes without alerting again.
	for range 3 {
		env.now = env.now.Add(middleware.LoginIPLockout - time.Second)
		assertLocked(t, env.login(authTestUser, authTestPass, ip), "300")
	}
	if len(env.rec.Alerts()) != 1 {
		t.Fatalf("extensions alerted again: %v", alertIDs(env.rec))
	}
	// Another address is unaffected.
	if other := env.login(authTestUser, authTestPass, "127.0.0.1"); other.Code != http.StatusNoContent {
		t.Fatalf("other address login = %d", other.Code)
	}

	// Five quiet minutes end the lock.
	env.now = env.now.Add(middleware.LoginIPLockout + time.Second)
	if rec := env.login(authTestUser, authTestPass, ip); rec.Code != http.StatusNoContent {
		t.Fatalf("login after lock expiry = %d", rec.Code)
	}

	// A new lock after that one expired alerts again.
	for i := range middleware.LoginIPFailures {
		env.login(fmt.Sprintf("again%d", i), "wrong password", ip)
	}
	if ids := alertIDs(env.rec); len(ids) != 2 || ids[1] != "auth-lockout-ip" {
		t.Fatalf("second lock start alerts = %v", ids)
	}
}

func TestAuthLogin_AccountLockout(t *testing.T) {
	for _, user := range []string{authTestUser, "nobody-by-this-name"} {
		t.Run(user, func(t *testing.T) {
			env := newAuthEnv(t, true)
			// Different addresses so only the username threshold is reached.
			for i := range middleware.LoginUserFailures {
				if rec := env.login(user, "wrong password", fmt.Sprintf("198.51.100.%d", i)); rec.Code != http.StatusUnauthorized {
					t.Fatalf("failure %d: status = %d", i+1, rec.Code)
				}
			}
			alerts := env.rec.Alerts()
			if len(alerts) != 1 || alerts[0].EventID != "auth-lockout-account" || alerts[0].Priority != alerter.Normal {
				t.Fatalf("alerts after lockout = %+v", alerts)
			}
			// Locked from any address, right password or not; the response is
			// the same whether the account exists or not.
			for range 3 {
				env.now = env.now.Add(middleware.LoginUserLockout - time.Second)
				assertLocked(t, env.login(user, authTestPass, "192.0.2.50"), "600")
			}
			if len(env.rec.Alerts()) != 1 {
				t.Fatalf("extensions alerted again: %v", alertIDs(env.rec))
			}
			// Other usernames from the same address are unaffected.
			if rec := env.login("someone-else", "x", "192.0.2.50"); rec.Code != http.StatusUnauthorized {
				t.Fatalf("other username = %d, want 401", rec.Code)
			}
			env.now = env.now.Add(middleware.LoginUserLockout + time.Second)
			rec := env.login(user, authTestPass, "192.0.2.50")
			if user == authTestUser && rec.Code != http.StatusNoContent {
				t.Fatalf("login after lock expiry = %d", rec.Code)
			}
			if user != authTestUser && rec.Code != http.StatusUnauthorized {
				t.Fatalf("unknown user after lock expiry = %d, want 401", rec.Code)
			}
		})
	}
}

func TestAuthLogin_SuccessClearsCounters(t *testing.T) {
	env := newAuthEnv(t, true)
	const ip = "203.0.113.8"
	for range middleware.LoginIPFailures - 1 {
		env.login(authTestUser, "wrong password", ip)
	}
	if rec := env.login(authTestUser, authTestPass, ip); rec.Code != http.StatusNoContent {
		t.Fatalf("login = %d", rec.Code)
	}
	for range middleware.LoginIPFailures - 1 {
		env.login(authTestUser, "wrong password", ip)
	}
	if rec := env.login(authTestUser, authTestPass, ip); rec.Code != http.StatusNoContent {
		t.Fatalf("login after success reset = %d (counters not cleared)", rec.Code)
	}
	if len(env.rec.Alerts()) != 0 {
		t.Fatalf("alerts = %v", alertIDs(env.rec))
	}
}

// Running `claw admin` rewrites credentials.json; the next login attempt sees
// the change and clears every lock, so the new password works immediately.
func TestAuthLogin_CredentialsChangeClearsAllLocks(t *testing.T) {
	env := newAuthEnv(t, true)
	const ip = "203.0.113.9"
	for range middleware.LoginIPFailures {
		env.login(authTestUser, "wrong password", ip)
	}
	for i := range middleware.LoginUserFailures {
		env.login("other", "wrong password", fmt.Sprintf("198.51.100.%d", i))
	}
	assertLocked(t, env.login(authTestUser, authTestPass, ip), "600")
	assertLocked(t, env.login("other", "x", "192.0.2.1"), "600")

	const newPass = "the operator's brand new password"
	if err := admin.Write(env.store.Path(), authTestUser, newPass); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(env.store.Path(), time.Now(), time.Now().Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if rec := env.login(authTestUser, newPass, ip); rec.Code != http.StatusNoContent {
		t.Fatalf("login with the new password after claw admin = %d %s", rec.Code, rec.Body.String())
	}
	if rec := env.login("other", "x", "192.0.2.1"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("account lock survived the credentials change: %d", rec.Code)
	}
}

// TestAuthLogin_LockoutExempt: an address in gateway.lockout_exempt is never
// locked by address, the account lock still applies from it, a bad list is
// refused and keeps the previous one, and a new list replaces the old.
func TestAuthLogin_LockoutExempt(t *testing.T) {
	env := newAuthEnv(t, true)
	const proxy = "192.0.2.10"
	if err := env.h.SetLockoutExempt([]string{"192.0.2.0/24"}); err != nil {
		t.Fatal(err)
	}

	// Many failures under different usernames: no address lock, no alert.
	for i := range 3 * middleware.LoginIPFailures {
		if rec := env.login(fmt.Sprintf("guess%d", i), "wrong password", proxy); rec.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d: status = %d", i+1, rec.Code)
		}
	}
	if len(env.rec.Alerts()) != 0 {
		t.Fatalf("exempt address alerted: %v", alertIDs(env.rec))
	}
	if rec := env.login(authTestUser, authTestPass, proxy); rec.Code != http.StatusNoContent {
		t.Fatalf("exempt address login = %d", rec.Code)
	}

	// The account lock still starts and applies from the exempt address.
	for range middleware.LoginUserFailures {
		env.login(authTestUser, "wrong password", proxy)
	}
	if ids := alertIDs(env.rec); len(ids) != 1 || ids[0] != "auth-lockout-account" {
		t.Fatalf("alerts = %v, want one account lockout", ids)
	}
	assertLocked(t, env.login(authTestUser, authTestPass, proxy), "600")

	// A bad list is refused and the previous one stays.
	if err := env.h.SetLockoutExempt([]string{"192.0.2.0/99"}); err == nil {
		t.Fatal("bad entry accepted")
	}
	for i := range 2 * middleware.LoginIPFailures {
		env.login(fmt.Sprintf("more%d", i), "wrong password", proxy)
	}
	if rec := env.login("someone", "x", proxy); rec.Code != http.StatusUnauthorized {
		t.Fatalf("exemption lost after a rejected list: status = %d", rec.Code)
	}

	// A new list replaces the old: the proxy now locks like any address.
	if err := env.h.SetLockoutExempt([]string{"198.51.100.1"}); err != nil {
		t.Fatal(err)
	}
	for i := range middleware.LoginIPFailures {
		env.login(fmt.Sprintf("last%d", i), "wrong password", proxy)
	}
	assertLocked(t, env.login("someone", "x", proxy), "300")
}
