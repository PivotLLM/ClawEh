// ClawEh
// License: MIT

package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/app"
	"github.com/PivotLLM/ClawEh/internal/clock"
)

// pageHarness is a started BrowserSetup with a fake clock and an HTTP client
// that does not follow redirects.
type pageHarness struct {
	t      *testing.T
	setup  *BrowserSetup
	clk    *clock.Fake
	out    bytes.Buffer
	opened []string
	client *http.Client
	base   string // http://127.0.0.1:<port>
	code   string
}

func startPage(t *testing.T) *pageHarness {
	t.Helper()
	h := &pageHarness{t: t, clk: clock.NewFake(time.Unix(1_800_000_000, 0))}
	s, err := StartBrowserSetup(BrowserSetupOptions{
		Clock: h.clk,
		Out:   &h.out,
		Open:  func(link string) bool { h.opened = append(h.opened, link); return true },
	})
	if err != nil {
		t.Fatalf("StartBrowserSetup: %v", err)
	}
	t.Cleanup(s.shutdown)
	h.setup = s
	u, err := url.Parse(s.URL())
	if err != nil {
		t.Fatal(err)
	}
	h.base = "http://" + u.Host
	h.code = u.Query().Get("code")
	h.client = &http.Client{
		Timeout:       5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return h
}

// reply is a response with its body read and closed.
type reply struct {
	StatusCode int
	Header     http.Header
}

func (h *pageHarness) do(req *http.Request) (reply, string) {
	h.t.Helper()
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatalf("%s %s: %v", req.Method, req.URL, err)
	}
	body, err := io.ReadAll(resp.Body)
	if cerr := resp.Body.Close(); cerr != nil {
		h.t.Errorf("close body: %v", cerr)
	}
	if err != nil {
		h.t.Fatal(err)
	}
	return reply{StatusCode: resp.StatusCode, Header: resp.Header}, string(body)
}

// answers reports whether the page still accepts requests.
func (h *pageHarness) answers() bool {
	h.t.Helper()
	resp, err := h.client.Get(h.base + "/?code=" + h.code)
	if err != nil {
		return false
	}
	if cerr := resp.Body.Close(); cerr != nil {
		h.t.Errorf("close body: %v", cerr)
	}
	return true
}

func (h *pageHarness) get(path string) (reply, string) {
	h.t.Helper()
	req, err := http.NewRequest(http.MethodGet, h.base+path, nil)
	if err != nil {
		h.t.Fatal(err)
	}
	return h.do(req)
}

func (h *pageHarness) post(form url.Values, edit func(*http.Request)) (reply, string) {
	h.t.Helper()
	req, err := http.NewRequest(http.MethodPost, h.base+"/", strings.NewReader(form.Encode()))
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// What a browser sends with this page's form: the no-referrer policy
	// turns the Origin of a form POST into null.
	req.Header.Set("Origin", "null")
	if edit != nil {
		edit(req)
	}
	return h.do(req)
}

func (h *pageHarness) status() statusReply {
	h.t.Helper()
	resp, body := h.get("/status?code=" + url.QueryEscape(h.code))
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("status: %d %s", resp.StatusCode, body)
	}
	var s statusReply
	if err := json.Unmarshal([]byte(body), &s); err != nil {
		h.t.Fatalf("status body %q: %v", body, err)
	}
	return s
}

func (h *pageHarness) form(username, password, repeat string) url.Values {
	return url.Values{"code": {h.code}, "username": {username}, "password": {password}, "repeat": {repeat}}
}

func TestBrowserSetup_PrintsLinkAndOpensIt(t *testing.T) {
	h := startPage(t)
	link := h.setup.URL()
	if !regexp.MustCompile(`^http://127\.0\.0\.1:\d+/\?code=[A-Za-z0-9_-]{43}$`).MatchString(link) {
		t.Fatalf("link = %q", link)
	}
	if !strings.Contains(h.out.String(), "Open this page to create the admin account:\n"+link+"\n") {
		t.Errorf("output lacks the link on its own line:\n%s", h.out.String())
	}
	if len(h.opened) != 1 || h.opened[0] != link {
		t.Errorf("opened %q; want exactly [%q]", h.opened, link)
	}
}

func TestBrowserSetup_CodeRequired(t *testing.T) {
	h := startPage(t)
	for _, path := range []string{"/", "/?code=", "/?code=wrong", "/?code=" + h.code + "x", "/status", "/status?code=wrong", "/other?code=" + h.code} {
		resp, body := h.get(path)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s = %d; want 404", path, resp.StatusCode)
		}
		if strings.Contains(body, "Create account") || strings.Contains(body, "state") {
			t.Errorf("GET %s leaked the page: %q", path, body)
		}
	}
	form := h.form("alice", "alice's long password", "alice's long password")
	form.Set("code", "wrong")
	if resp, _ := h.post(form, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("POST with a wrong code = %d; want 404", resp.StatusCode)
	}
	if s := h.status(); s.State != stateWaiting {
		t.Errorf("state = %q after a refused POST; want waiting", s.State)
	}
}

func TestBrowserSetup_RefusesWrongHost(t *testing.T) {
	h := startPage(t)
	for _, host := range []string{"localhost" + strings.TrimPrefix(h.base, "http://127.0.0.1"), "127.0.0.1", "evil.example"} {
		req, err := http.NewRequest(http.MethodGet, h.base+"/?code="+h.code, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = host
		if resp, body := h.do(req); resp.StatusCode != http.StatusMisdirectedRequest || strings.Contains(body, "Create account") {
			t.Errorf("Host %q = %d %q; want 421 without the page", host, resp.StatusCode, body)
		}
	}
}

// A browser sends "Origin: null" with this page's form, because the page's
// referrer policy is no-referrer. Refusing it made the form unusable.
func TestBrowserSetup_AcceptsTheFormAsABrowserSendsIt(t *testing.T) {
	h := startPage(t)
	if resp, _ := h.get("/?code=" + url.QueryEscape(h.code)); resp.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("Referrer-Policy = %q; this test assumes no-referrer", resp.Header.Get("Referrer-Policy"))
	}
	resp, body := h.post(h.form("alice", "alice's long password", "alice's long password"), func(r *http.Request) {
		r.Header.Set("Origin", "null")
	})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("form with Origin null = %d %q; want 303", resp.StatusCode, body)
	}
	if s := h.status(); s.State != stateWorking {
		t.Fatalf("state = %q after the form; want %q", s.State, stateWorking)
	}
}

// Another site cannot read the page, so it cannot send the one-time code.
func TestBrowserSetup_RefusesPostWithoutTheCode(t *testing.T) {
	h := startPage(t)
	for _, code := range []string{"", "wrong", h.code + "x"} {
		form := h.form("alice", "alice's long password", "alice's long password")
		form.Set("code", code)
		resp, body := h.post(form, func(r *http.Request) {
			r.Header.Set("Origin", "http://evil.example")
		})
		if resp.StatusCode != http.StatusNotFound || strings.Contains(body, "Create account") {
			t.Errorf("POST with code %q = %d %q; want 404 without the page", code, resp.StatusCode, body)
		}
	}
	if s := h.status(); s.State != stateWaiting {
		t.Fatalf("state = %q after POSTs without the code; want waiting", s.State)
	}
}

func TestBrowserSetup_BodyLimit(t *testing.T) {
	h := startPage(t)
	form := h.form("alice", "alice's long password", "alice's long password")
	form.Set("filler", strings.Repeat("a", browserBodyLimit))
	if resp, _ := h.post(form, nil); resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized POST = %d; want 413", resp.StatusCode)
	}
}

func TestBrowserSetup_SecurityHeaders(t *testing.T) {
	h := startPage(t)
	check := func(name string, resp reply) {
		t.Helper()
		for header, want := range map[string]string{
			"Cache-Control":          "no-store",
			"Referrer-Policy":        "no-referrer",
			"X-Content-Type-Options": "nosniff",
			"X-Frame-Options":        "DENY",
		} {
			if got := resp.Header.Get(header); got != want {
				t.Errorf("%s: %s = %q; want %q", name, header, got, want)
			}
		}
		csp := resp.Header.Get("Content-Security-Policy")
		if !strings.Contains(csp, "default-src 'none'") || strings.Contains(csp, "http") ||
			strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "*") {
			t.Errorf("%s: Content-Security-Policy = %q", name, csp)
		}
	}
	resp, body := h.get("/?code=" + h.code)
	check("form", resp)
	nonce := regexp.MustCompile(`'nonce-([^']+)'`).FindStringSubmatch(resp.Header.Get("Content-Security-Policy"))
	if nonce == nil || !strings.Contains(body, `<style nonce="`+nonce[1]+`">`) {
		t.Errorf("the page's style does not carry the policy's nonce")
	}
	if strings.Contains(body, "src=") || strings.Contains(body, `href="http`) {
		t.Errorf("the page loads something external:\n%s", body)
	}
	resp, _ = h.get("/nope")
	check("not found", resp)
	resp, _ = h.get("/status?code=" + h.code)
	check("status", resp)
}

func TestBrowserSetup_FormText(t *testing.T) {
	h := startPage(t)
	_, body := h.get("/?code=" + h.code)
	for _, want := range []string{
		"<title>Create the admin account</title>",
		"You sign in to " + app.Name() + " with this account.",
		`autocomplete="username"`,
		`autocomplete="new-password"`,
		"Repeat password",
		fmt.Sprintf("At least %d characters.", MinPasswordLength),
		"Create account",
		`name="code" value="` + h.code + `"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("form lacks %q", want)
		}
	}
}

func TestBrowserSetup_ValidationProblems(t *testing.T) {
	for _, tc := range []struct {
		name, username, password, repeat, want string
	}{
		{"short password", "alice", "short", "short", fmt.Sprintf("Password must be at least %d characters.", MinPasswordLength)},
		{"mismatch", "alice", "alice's long password", "alice's other password", "Passwords do not match."},
		{"whitespace in username", "al ice", "alice's long password", "alice's long password", "Username must not contain whitespace."},
		{"no username", "  ", "alice's long password", "alice's long password", "Username is required."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := startPage(t)
			resp, body := h.post(h.form(tc.username, tc.password, tc.repeat), nil)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("POST = %d; want 400", resp.StatusCode)
			}
			if !strings.Contains(body, tc.want) {
				t.Errorf("body lacks %q:\n%s", tc.want, body)
			}
			if strings.TrimSpace(tc.username) != "" && !strings.Contains(body, `value="`+tc.username+`"`) {
				t.Errorf("username %q not kept", tc.username)
			}
			if strings.Contains(body, tc.password) || strings.Contains(body, tc.repeat) {
				t.Errorf("a password was echoed back")
			}
			if s := h.status(); s.State != stateWaiting {
				t.Errorf("state = %q; want waiting", s.State)
			}
			select {
			case <-h.setup.submitted:
				t.Errorf("an invalid submission was handed on")
			default:
			}
		})
	}
}

func TestBrowserSetup_AcceptsOneSubmissionAndReportsProgress(t *testing.T) {
	h := startPage(t)
	resp, _ := h.post(h.form(" alice ", "alice's long password", "alice's long password"), nil)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/?code="+h.code {
		t.Fatalf("POST = %d Location %q; want 303 to the page", resp.StatusCode, resp.Header.Get("Location"))
	}
	user, pass, err := h.setup.Wait(context.Background())
	if err != nil || user != "alice" || pass != "alice's long password" {
		t.Fatalf("Wait = %q, %v; want alice and her password", user, err)
	}

	resp, body := h.post(h.form("bob", "bob's long password", "bob's long password"), nil)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(body, "This page has already been used.") {
		t.Fatalf("second POST = %d %q; want 409", resp.StatusCode, body)
	}

	_, body = h.get("/?code=" + h.code)
	if !strings.Contains(body, "Creating the account…") || strings.Contains(body, "Create account<") {
		t.Errorf("page after submitting is not the progress view:\n%s", body)
	}
	if s := h.status(); s.State != stateWorking || s.Message != "Creating the account…" {
		t.Errorf("status = %+v; want working, creating", s)
	}
	h.setup.AccountCreated()
	if s := h.status(); s.State != stateWorking || s.Message != "Starting "+app.ShortName()+"…" {
		t.Errorf("status = %+v; want working, starting", s)
	}
	h.setup.Started("http://127.0.0.1:18790/")
	if s := h.status(); s.State != stateStarted || s.URL != "http://127.0.0.1:18790/" {
		t.Errorf("status = %+v; want started with the WebUI address", s)
	}

	done := make(chan struct{})
	go func() { h.setup.Close(context.Background()); close(done) }()
	select {
	case <-done: // the final state was served, so no grace is needed
	case <-time.After(5 * time.Second):
		t.Fatal("Close kept waiting although the final state was served")
	}
	if h.answers() {
		t.Errorf("the page still answers after Close")
	}
}

func TestBrowserSetup_CloseWaitsForGrace(t *testing.T) {
	h := startPage(t)
	h.post(h.form("alice", "alice's long password", "alice's long password"), nil)
	if _, _, err := h.setup.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.setup.SaveFailed()
	done := make(chan struct{})
	go func() { h.setup.Close(context.Background()); close(done) }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.clk.BlockUntil(ctx, 1); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
		t.Fatal("Close returned before the grace although nothing fetched the final state")
	default:
	}
	h.clk.Advance(finalStateGrace)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return after the grace")
	}
}

func TestBrowserSetup_TimesOut(t *testing.T) {
	h := startPage(t)
	result := make(chan error, 1)
	go func() {
		_, _, err := h.setup.Wait(context.Background())
		result <- err
	}()
	h.clk.Advance(BrowserSetupLimit - time.Second)
	select {
	case err := <-result:
		t.Fatalf("Wait returned early: %v", err)
	default:
	}
	h.clk.Advance(time.Second)
	select {
	case err := <-result:
		if !errors.Is(err, ErrBrowserSetupTimedOut) {
			t.Fatalf("Wait = %v; want ErrBrowserSetupTimedOut", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not return at the limit")
	}
	if h.answers() {
		t.Errorf("the page still answers after the limit")
	}
}

func TestBrowserSetup_CancelEndsWait(t *testing.T) {
	h := startPage(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := h.setup.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait = %v; want context.Canceled", err)
	}
	if h.answers() {
		t.Errorf("the page still answers after cancel")
	}
}

func TestEnsureAccount_AskWritesAndChowns(t *testing.T) {
	home := t.TempDir()
	var chowned []string
	user, created, err := EnsureAccount(EnsureOptions{
		Home:         home,
		Getenv:       noEnv,
		OpenTerminal: noTerminal(t),
		FileMode:     0o640,
		Ask:          func() (string, string, error) { return "alice", "alice's long password", nil },
		Chown:        func(path string) error { chowned = append(chowned, path); return nil },
	})
	if err != nil || !created || user != "alice" {
		t.Fatalf("EnsureAccount = %q, %v, %v", user, created, err)
	}
	fi, err := os.Stat(Path(home))
	if err != nil || fi.Mode().Perm() != 0o640 {
		t.Fatalf("credentials %v, %v; want mode 0640", fi, err)
	}
	creds, err := Load(Path(home), 0o640)
	if err != nil || !creds.Verify("alice", "alice's long password") {
		t.Fatalf("Load/Verify: %v", err)
	}
	if len(chowned) != 1 || chowned[0] != Path(home) {
		t.Errorf("Chown calls %q; want [%q]", chowned, Path(home))
	}
}

func TestEnsureAccount_AskNotUsedWhenAccountOrEnv(t *testing.T) {
	ask := func() (string, string, error) {
		t.Error("Ask called")
		return "", "", errors.New("unexpected")
	}
	home := t.TempDir()
	if err := Write(Path(home), "alice", "alice's long password", DefaultFileMode); err != nil {
		t.Fatal(err)
	}
	if user, created, err := EnsureAccount(EnsureOptions{Home: home, Getenv: noEnv, Ask: ask}); err != nil || created || user != "alice" {
		t.Errorf("existing account: %q, %v, %v", user, created, err)
	}
	home = t.TempDir()
	if user, created, err := EnsureAccount(EnsureOptions{Home: home, Getenv: env("bob", "bob's long password"), Ask: ask}); err != nil || !created || user != "bob" {
		t.Errorf("environment: %q, %v, %v", user, created, err)
	}
}

func TestEnsureAccount_AskErrorWritesNothing(t *testing.T) {
	home := t.TempDir()
	_, _, err := EnsureAccount(EnsureOptions{
		Home: home, Getenv: noEnv,
		Ask: func() (string, string, error) { return "", "", ErrBrowserSetupTimedOut },
	})
	if !errors.Is(err, ErrBrowserSetupTimedOut) {
		t.Fatalf("EnsureAccount = %v; want ErrBrowserSetupTimedOut", err)
	}
	if _, err := os.Stat(Path(home)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("credentials file: %v; want none", err)
	}
}

func TestSentence(t *testing.T) {
	for in, want := range map[string]string{
		"username is required": "Username is required.",
		"already a sentence.":  "Already a sentence.",
		"":                     "",
	} {
		if got := sentence(in); got != want {
			t.Errorf("sentence(%q) = %q; want %q", in, got, want)
		}
	}
}

func TestBrowserSetup_FailureSentencesAreFixed(t *testing.T) {
	h := startPage(t)
	h.setup.SaveFailed()
	if s := h.status(); s.State != stateFailed || s.Message != "The account could not be saved; run the installer again." {
		t.Errorf("save failed: %+v", s)
	}
	h.setup.StartFailed()
	if s := h.status(); s.State != stateFailed || s.Message != app.ShortName()+" could not be started; run the installer again." {
		t.Errorf("start failed: %+v", s)
	}
}

func TestBrowserSetup_LinkNotPrintedWithoutTerminal(t *testing.T) {
	var opened []string
	s, err := StartBrowserSetup(BrowserSetupOptions{
		Clock: clock.NewFake(time.Unix(1_800_000_000, 0)),
		Open:  func(link string) bool { opened = append(opened, link); return true },
	})
	if err != nil {
		t.Fatalf("StartBrowserSetup: %v", err)
	}
	t.Cleanup(s.shutdown)
	if len(opened) != 1 || opened[0] != s.URL() {
		t.Errorf("opened %q; want [%q]", opened, s.URL())
	}
}

func TestBrowserSetup_UnreachableFailsAtOnce(t *testing.T) {
	clk := clock.NewFake(time.Unix(1_800_000_000, 0))
	var link string
	s, err := StartBrowserSetup(BrowserSetupOptions{
		Clock: clk,
		Open:  func(l string) bool { link = l; return false },
	})
	if !errors.Is(err, ErrBrowserSetupUnreachable) || s != nil {
		t.Fatalf("StartBrowserSetup = %v, %v; want ErrBrowserSetupUnreachable", s, err)
	}
	if clk.Waiters() != 0 {
		t.Errorf("the limit timer is still running")
	}
	if resp, err := http.Get(link); err == nil { //nolint:noctx // a plain probe of a closed page
		if cerr := resp.Body.Close(); cerr != nil {
			t.Error(cerr)
		}
		t.Errorf("the page answers although nobody can reach it")
	}
}

func TestBrowserSetup_ConcurrentSubmissionsAcceptOne(t *testing.T) {
	h := startPage(t)
	const n = 8
	codes := make(chan int, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			user := fmt.Sprintf("alice%d", i)
			resp, _ := h.post(h.form(user, "alice's long password", "alice's long password"), nil)
			codes <- resp.StatusCode
		})
	}
	wg.Wait()
	close(codes)
	accepted, refused := 0, 0
	for c := range codes {
		switch c {
		case http.StatusSeeOther:
			accepted++
		case http.StatusConflict:
			refused++
		default:
			t.Errorf("unexpected status %d", c)
		}
	}
	if accepted != 1 || refused != n-1 {
		t.Fatalf("accepted %d, refused %d; want 1 and %d", accepted, refused, n-1)
	}
	if _, _, err := h.setup.Wait(context.Background()); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

func TestBrowserSetup_SubmissionRacingTheLimitCounts(t *testing.T) {
	h := startPage(t)
	h.post(h.form("alice", "alice's long password", "alice's long password"), nil)
	// The limit fires after the submission was accepted but before Wait
	// took it: the branch Wait runs when its select picks the limit.
	user, _, err := h.setup.limitReached()
	if err != nil || user != "alice" {
		t.Fatalf("Wait = %q, %v; want alice", user, err)
	}
	if !h.answers() {
		t.Errorf("the page closed although the submission counted")
	}
}

func TestBrowserSetup_UsernameEscaped(t *testing.T) {
	h := startPage(t)
	_, body := h.post(h.form(`"><script>alert(1)</script>`, "short", "short"), nil)
	if strings.Contains(body, `"><script>alert(1)`) {
		t.Fatalf("username came back unescaped:\n%s", body)
	}
	if !strings.Contains(body, `&#34;&gt;&lt;script&gt;alert(1)&lt;/script&gt;`) {
		t.Errorf("username not kept escaped:\n%s", body)
	}
}

func TestBrowserSetup_LimitFitsTheInstaller(t *testing.T) {
	// The macOS Installer kills a package's script after 10 minutes, and
	// the service start must fit after the wait.
	if BrowserSetupLimit > 5*time.Minute {
		t.Errorf("BrowserSetupLimit = %v; want at most 5 minutes", BrowserSetupLimit)
	}
	if !strings.Contains(ErrBrowserSetupTimedOut.Error(), "5 minutes") {
		t.Errorf("ErrBrowserSetupTimedOut = %q; want it to say 5 minutes", ErrBrowserSetupTimedOut)
	}
}
