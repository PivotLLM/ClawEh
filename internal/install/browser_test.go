package install

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/app"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/internal/admin"
	"github.com/PivotLLM/ClawEh/internal/clock"
)

var linkPattern = regexp.MustCompile(`http://127\.0\.0\.1:\d+/\?code=[A-Za-z0-9_-]+`)

// pageEnv is a --browser-setup wiring for tests: the opener records the
// link and hands it on.
type pageEnv struct {
	t       *testing.T
	browser *browserSetup
	links   chan string

	mu        sync.Mutex
	opened    []string // links the opener was given
	openOK    bool
	intercept bool // Ctrl+C is being intercepted
}

func newPageEnv(t *testing.T, clk clock.Clock) *pageEnv {
	t.Helper()
	e := &pageEnv{t: t, links: make(chan string, 4), openOK: true}
	e.browser = &browserSetup{
		clock: clk,
		open:  e.open,
		probe: probeHealth,
		interrupts: func(parent context.Context) (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(parent)
			e.setIntercept(true)
			return ctx, func() { e.setIntercept(false); cancel() }
		},
	}
	return e
}

func (e *pageEnv) open(_ context.Context, _ *TargetUser, link string) bool {
	e.mu.Lock()
	e.opened = append(e.opened, link)
	ok := e.openOK
	e.mu.Unlock()
	if ok {
		e.links <- link
	}
	return ok
}

func (e *pageEnv) setIntercept(on bool) {
	e.mu.Lock()
	e.intercept = on
	e.mu.Unlock()
}

func (e *pageEnv) intercepting() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.intercept
}

func (e *pageEnv) openedLinks() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.opened)
}

func (e *pageEnv) awaitLink() string {
	e.t.Helper()
	select {
	case link := <-e.links:
		if !linkPattern.MatchString(link) {
			e.t.Fatalf("opened %q; want the page's link", link)
		}
		return link
	case <-time.After(5 * time.Second):
		e.t.Fatal("the browser was never opened")
		return ""
	}
}

// stubServiceSeams replaces registerService and the terminal, restoring
// both after the test (and after the installer goroutine, see runProvision).
func stubServiceSeams(t *testing.T, register func(*TargetUser, string, string, string) error) {
	t.Helper()
	origRegister, origTerminal := registerService, adminTerminal
	t.Cleanup(func() { registerService, adminTerminal = origRegister, origTerminal })
	registerService = register
	adminTerminal = func(io.Writer) (*admin.Terminal, error) {
		t.Error("terminal opened although --browser-setup is on")
		return noTerminal(nil)
	}
}

func noRegister(t *testing.T) func(*TargetUser, string, string, string) error {
	t.Helper()
	return func(*TargetUser, string, string, string) error {
		t.Error("service registered")
		return nil
	}
}

type provisionResult struct {
	user         string
	notAnswering bool
	err          error
}

// runProvision runs provisionAndRegister in a goroutine. A cleanup,
// registered after the seams are stubbed so it runs before they are
// restored, waits for it even when the test fails.
func runProvision(t *testing.T, tu *TargetUser, clawHome string, browser *browserSetup) <-chan provisionResult {
	t.Helper()
	done := make(chan provisionResult, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		user, notAnswering, err := provisionAndRegister(tu, "/nonexistent/claw", "/nonexistent", clawHome, false, browser)
		done <- provisionResult{user, notAnswering, err}
	}()
	t.Cleanup(func() {
		select {
		case <-finished:
		case <-time.After(30 * time.Second):
			t.Error("provisionAndRegister still running at cleanup")
		}
	})
	return done
}

func awaitProvision(t *testing.T, done <-chan provisionResult) provisionResult {
	t.Helper()
	select {
	case r := <-done:
		return r
	case <-time.After(15 * time.Second):
		t.Fatal("provisionAndRegister did not return")
		return provisionResult{}
	}
}

// webUIListener stands in for the started service (its /health answers
// 200) and writes a config.json placing the WebUI on it; it returns the
// WebUI address.
func webUIListener(t *testing.T, clawHome string) string {
	t.Helper()
	srv := httptest.NewServer(healthHandler(http.StatusOK))
	t.Cleanup(srv.Close)
	addr, ok := srv.Listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("listener address %v is not TCP", srv.Listener.Addr())
	}
	return writeGatewayPort(t, clawHome, addr.Port)
}

func healthHandler(status int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
	})
}

func writeGatewayPort(t *testing.T, clawHome string, port int) string {
	t.Helper()
	if err := os.MkdirAll(clawHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clawHome, "config.json"), fmt.Appendf(nil, `{"gateway":{"port":%d}}`, port), 0o600); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("http://127.0.0.1:%d/", port)
}

type pageStatus struct {
	State   string `json:"state"`
	Message string `json:"message"`
	URL     string `json:"url"`
}

var pageClient = &http.Client{
	Timeout:       5 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func submitAccount(t *testing.T, link, username, password string) {
	t.Helper()
	u, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{"code": {u.Query().Get("code")}, "username": {username}, "password": {password}, "repeat": {password}}
	resp, err := pageClient.PostForm("http://"+u.Host+"/", form)
	if err != nil {
		t.Fatal(err)
	}
	if cerr := resp.Body.Close(); cerr != nil {
		t.Errorf("close body: %v", cerr)
	}
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST = %d; want 303", resp.StatusCode)
	}
}

func fetchStatus(t *testing.T, link string) pageStatus {
	t.Helper()
	u, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := pageClient.Get("http://" + u.Host + "/status?code=" + url.QueryEscape(u.Query().Get("code")))
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	var s pageStatus
	err = json.NewDecoder(resp.Body).Decode(&s)
	if cerr := resp.Body.Close(); cerr != nil {
		t.Errorf("close body: %v", cerr)
	}
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	return s
}

// awaitFinalState polls the status until it leaves the working states.
func awaitFinalState(t *testing.T, link string) pageStatus {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		s := fetchStatus(t, link)
		if s.State != "working" && s.State != "waiting" {
			return s
		}
		if time.Now().After(deadline) {
			t.Fatalf("status stuck at %+v", s)
		}
		time.Sleep(5 * time.Millisecond) // waiting for the installer goroutine, not timing it
	}
}

// pageAnswers reports whether the page at link still accepts requests.
func pageAnswers(t *testing.T, link string) bool {
	t.Helper()
	resp, err := pageClient.Get(link)
	if err != nil {
		return false
	}
	if cerr := resp.Body.Close(); cerr != nil {
		t.Errorf("close body: %v", cerr)
	}
	return true
}

func clearAdminEnv(t *testing.T) {
	t.Helper()
	t.Setenv(admin.EnvUser, "")
	t.Setenv(admin.EnvPassword, "")
}

// lineWriter hands every write on as a string.
type lineWriter chan string

func (w lineWriter) Write(p []byte) (int, error) {
	w <- string(p)
	return len(p), nil
}

func TestProvisionAndRegister_BrowserSetupOffUnchanged(t *testing.T) {
	clearAdminEnv(t)
	stubServiceSeams(t, noRegister(t))
	adminTerminal = noTerminal
	clawHome := filepath.Join(t.TempDir(), "claw")

	_, _, err := provisionAndRegister(&TargetUser{Username: "alice"}, "/nonexistent/claw", "/nonexistent", clawHome, false, nil)
	if !errors.Is(err, admin.ErrNoAccountNoTerminal) {
		t.Fatalf("provisionAndRegister = %v; want ErrNoAccountNoTerminal", err)
	}
}

func TestProvisionAndRegister_BrowserSetupKeepsExistingAccount(t *testing.T) {
	clearAdminEnv(t)
	var registered int
	stubServiceSeams(t, func(*TargetUser, string, string, string) error { registered++; return nil })
	env := newPageEnv(t, nil)
	clawHome := t.TempDir()
	if err := admin.Write(admin.Path(clawHome), "alice", "alice's long password", admin.DefaultFileMode); err != nil {
		t.Fatal(err)
	}

	user, _, err := provisionAndRegister(&TargetUser{Username: "alice"}, "/nonexistent/claw", "/nonexistent", clawHome, true, env.browser)
	if err != nil || user != "alice" || registered != 1 {
		t.Fatalf("provisionAndRegister = %q, %v, registrations %d; want alice, nil, 1", user, err, registered)
	}
	if got := env.openedLinks(); len(got) != 0 {
		t.Fatalf("page offered although an account exists: %q", got)
	}
	if env.intercepting() {
		t.Error("Ctrl+C still intercepted")
	}
}

func TestProvisionAndRegister_BrowserSetupEnvironmentWins(t *testing.T) {
	t.Setenv(admin.EnvUser, "bob")
	t.Setenv(admin.EnvPassword, "bob's long password")
	var registered int
	stubServiceSeams(t, func(*TargetUser, string, string, string) error { registered++; return nil })
	env := newPageEnv(t, nil)
	clawHome := filepath.Join(t.TempDir(), "claw")

	user, _, err := provisionAndRegister(&TargetUser{Username: "bob"}, "/nonexistent/claw", "/nonexistent", clawHome, false, env.browser)
	if err != nil || user != "bob" || registered != 1 {
		t.Fatalf("provisionAndRegister = %q, %v, registrations %d; want bob, nil, 1", user, err, registered)
	}
	if got := env.openedLinks(); len(got) != 0 {
		t.Fatalf("page offered although the environment supplies the account: %q", got)
	}
}

func TestProvisionAndRegister_BrowserSetupCreatesAccountThenStarts(t *testing.T) {
	clearAdminEnv(t)
	clawHome := filepath.Join(t.TempDir(), "claw")
	webUI := webUIListener(t, clawHome)
	env := newPageEnv(t, nil)
	entered, release := make(chan struct{}), make(chan struct{})
	var registered int
	stubServiceSeams(t, func(_ *TargetUser, _, _, home string) error {
		registered++
		if _, err := admin.Load(admin.Path(home), admin.DefaultFileMode); err != nil {
			t.Errorf("service registered before the account was written: %v", err)
		}
		if env.intercepting() {
			t.Error("Ctrl+C still intercepted while the service starts")
		}
		close(entered)
		<-release
		return nil
	})

	done := runProvision(t, &TargetUser{Username: "alice"}, clawHome, env.browser)
	link := env.awaitLink()
	submitAccount(t, link, "alice", "alice's long password")
	<-entered
	if s := fetchStatus(t, link); s.State != "working" || s.Message != "Starting "+app.ShortName()+"…" {
		t.Errorf("status while starting = %+v", s)
	}
	close(release)
	if s := awaitFinalState(t, link); s.State != "started" || s.URL != webUI {
		t.Errorf("final status = %+v; want started at %s", s, webUI)
	}

	r := awaitProvision(t, done)
	if r.notAnswering {
		t.Error("provisionAndRegister reported the WebUI not answering")
	}
	if r.err != nil || r.user != "alice" || registered != 1 {
		t.Fatalf("provisionAndRegister = %q, %v, registrations %d; want alice, nil, 1", r.user, r.err, registered)
	}
	fi, err := os.Stat(admin.Path(clawHome))
	if err != nil || fi.Mode().Perm() != admin.DefaultFileMode {
		t.Fatalf("credentials %v, %v; want mode %o", fi, err, admin.DefaultFileMode)
	}
	creds, err := admin.Load(admin.Path(clawHome), admin.DefaultFileMode)
	if err != nil || !creds.Verify("alice", "alice's long password") {
		t.Fatalf("Load/Verify: %v", err)
	}
	if got := env.openedLinks(); len(got) != 1 || got[0] != link {
		t.Errorf("opened %q; want once, %q", got, link)
	}
	if pageAnswers(t, link) {
		t.Errorf("the page still answers after the install")
	}
}

func TestProvisionAndRegister_BrowserSetupServiceFails(t *testing.T) {
	clearAdminEnv(t)
	clawHome := filepath.Join(t.TempDir(), "claw")
	env := newPageEnv(t, nil)
	stubServiceSeams(t, func(*TargetUser, string, string, string) error {
		return errors.New("launchctl bootstrap /Library/LaunchDaemons/x.plist: exit status 5")
	})

	done := runProvision(t, &TargetUser{Username: "alice"}, clawHome, env.browser)
	link := env.awaitLink()
	submitAccount(t, link, "alice", "alice's long password")
	if s := awaitFinalState(t, link); s.State != "failed" || s.Message != app.ShortName()+" could not be started; run the installer again." {
		t.Errorf("final status = %+v; want failed with the fixed sentence", s)
	}
	if r := awaitProvision(t, done); r.err == nil || !strings.Contains(r.err.Error(), "exit status 5") {
		t.Fatalf("provisionAndRegister = %v; want the service error", r.err)
	}
}

func TestProvisionAndRegister_BrowserSetupSaveFails(t *testing.T) {
	clearAdminEnv(t)
	clawHome := filepath.Join(t.TempDir(), "claw")
	env := newPageEnv(t, nil)
	env.openOK = false
	out := make(lineWriter, 16)
	env.browser.terminal = out
	stubServiceSeams(t, noRegister(t))
	// A uid that does not parse makes handing the file over fail.
	tu := &TargetUser{Username: "alice", UID: "x", GID: "x", IsRoot: true}

	done := runProvision(t, tu, clawHome, env.browser)
	var link string
	for link == "" {
		select {
		case line := <-out:
			link = linkPattern.FindString(line)
		case <-time.After(5 * time.Second):
			t.Fatal("the link was not printed")
		}
	}
	submitAccount(t, link, "alice", "alice's long password")
	if s := awaitFinalState(t, link); s.State != "failed" || s.Message != "The account could not be saved; run the installer again." {
		t.Errorf("final status = %+v; want failed with the fixed sentence", s)
	}
	r := awaitProvision(t, done)
	if r.err == nil || !strings.Contains(r.err.Error(), "uid") || !strings.Contains(r.err.Error(), "was not registered or started") {
		t.Fatalf("provisionAndRegister = %v; want the chown error, not registered", r.err)
	}
	if _, err := os.Stat(admin.Path(clawHome)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("credentials file: %v; want none", err)
	}
}

func TestProvisionAndRegister_BrowserSetupTimesOut(t *testing.T) {
	clearAdminEnv(t)
	clk := clock.NewFake(time.Unix(1_800_000_000, 0))
	clawHome := filepath.Join(t.TempDir(), "claw")
	env := newPageEnv(t, clk)
	stubServiceSeams(t, noRegister(t))

	done := runProvision(t, &TargetUser{Username: "alice"}, clawHome, env.browser)
	link := env.awaitLink()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := clk.BlockUntil(ctx, 1); err != nil {
		t.Fatal(err)
	}
	clk.Advance(admin.BrowserSetupLimit)

	r := awaitProvision(t, done)
	if !errors.Is(r.err, admin.ErrBrowserSetupTimedOut) || !strings.Contains(r.err.Error(), "was not registered or started") {
		t.Fatalf("provisionAndRegister = %v; want ErrBrowserSetupTimedOut, not registered", r.err)
	}
	if _, err := os.Stat(admin.Path(clawHome)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("credentials file: %v; want none", err)
	}
	if pageAnswers(t, link) {
		t.Errorf("the page still answers after the limit")
	}
	if env.intercepting() {
		t.Error("Ctrl+C still intercepted")
	}
}

func TestProvisionAndRegister_BrowserSetupCtrlC(t *testing.T) {
	clearAdminEnv(t)
	// Keep the test process alive whatever happens to the interception.
	guard := make(chan os.Signal, 1)
	signal.Notify(guard, os.Interrupt)
	t.Cleanup(func() { signal.Stop(guard) })

	clawHome := filepath.Join(t.TempDir(), "claw")
	env := newPageEnv(t, nil)
	notify := newBrowserSetup().interrupts
	env.browser.interrupts = func(parent context.Context) (context.Context, context.CancelFunc) {
		ctx, stop := notify(parent)
		env.setIntercept(true)
		return ctx, func() { env.setIntercept(false); stop() }
	}
	stubServiceSeams(t, noRegister(t))

	done := runProvision(t, &TargetUser{Username: "alice"}, clawHome, env.browser)
	link := env.awaitLink()
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	r := awaitProvision(t, done)
	if !errors.Is(r.err, context.Canceled) || !strings.Contains(r.err.Error(), "was not registered or started") {
		t.Fatalf("provisionAndRegister = %v; want cancelled, not registered", r.err)
	}
	if _, err := os.Stat(admin.Path(clawHome)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("credentials file: %v; want none", err)
	}
	if pageAnswers(t, link) {
		t.Errorf("the page still answers after Ctrl+C")
	}
	if env.intercepting() {
		t.Error("Ctrl+C still intercepted after the account step")
	}
}

func TestProvisionAndRegister_BrowserSetupUnreachableFailsAtOnce(t *testing.T) {
	clearAdminEnv(t)
	clawHome := filepath.Join(t.TempDir(), "claw")
	env := newPageEnv(t, clock.NewFake(time.Unix(1_800_000_000, 0)))
	env.openOK = false
	stubServiceSeams(t, noRegister(t))

	r := awaitProvision(t, runProvision(t, &TargetUser{Username: "alice"}, clawHome, env.browser))
	if !errors.Is(r.err, admin.ErrBrowserSetupUnreachable) || !strings.Contains(r.err.Error(), "was not registered or started") {
		t.Fatalf("provisionAndRegister = %v; want ErrBrowserSetupUnreachable, not registered", r.err)
	}
	if _, err := os.Stat(admin.Path(clawHome)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("credentials file: %v; want none", err)
	}
}

// TestProvisionAndRegister_BrowserSetupNoCodeOnNonTerminal runs the real
// wiring with the installer's output on a pipe: nothing printed may hold
// the one-time code.
func TestProvisionAndRegister_BrowserSetupNoCodeOnNonTerminal(t *testing.T) {
	clearAdminEnv(t)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origStdout := os.Stdout
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = origStdout })
	captured := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r) //nolint:errcheck // a pipe read to its end
		captured <- buf.String()
	}()

	clawHome := filepath.Join(t.TempDir(), "claw")
	webUIListener(t, clawHome)
	env := newPageEnv(t, nil)
	browser := newBrowserSetup()
	if browser.terminal != nil {
		t.Fatal("a pipe was taken for a terminal")
	}
	browser.open, browser.interrupts = env.browser.open, env.browser.interrupts
	stubServiceSeams(t, func(*TargetUser, string, string, string) error { return nil })

	done := runProvision(t, &TargetUser{Username: "alice"}, clawHome, browser)
	link := env.awaitLink()
	submitAccount(t, link, "alice", "alice's long password")
	if s := awaitFinalState(t, link); s.State != "started" {
		t.Errorf("final status = %+v; want started", s)
	}
	if res := awaitProvision(t, done); res.err != nil {
		t.Fatal(res.err)
	}
	os.Stdout = origStdout
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	out := <-captured
	code := strings.SplitN(link, "code=", 2)[1]
	if strings.Contains(out, code) || strings.Contains(out, "?code=") {
		t.Fatalf("the code was printed to a non-terminal:\n%s", out)
	}
}

func TestBrowserSetupFinish_WebUINotAnswering(t *testing.T) {
	clk := clock.NewFake(time.Unix(1_800_000_000, 0))
	clawHome := t.TempDir()
	webUI := writeGatewayPort(t, clawHome, 18790)
	var probed []string
	var mu sync.Mutex
	bs := &browserSetup{clock: clk, probe: func(_ context.Context, healthURL string) error {
		mu.Lock()
		probed = append(probed, healthURL)
		mu.Unlock()
		return errors.New("connection refused")
	}}

	setup, err := admin.StartBrowserSetup(admin.BrowserSetupOptions{Clock: clk, Out: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	submitAccount(t, setup.URL(), "alice", "alice's long password")
	if _, _, err := setup.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}

	finished := make(chan struct{})
	var answered bool
	go func() { answered = bs.finish(context.Background(), setup, clawHome); close(finished) }()
	t.Cleanup(func() {
		select {
		case <-finished:
		case <-time.After(30 * time.Second):
			t.Error("finish still running at cleanup")
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := clk.BlockUntil(ctx, 2); err != nil { // the wait's deadline and its first pause
		t.Fatal(err)
	}
	clk.Advance(webUIWait)
	s := awaitFinalState(t, setup.URL())
	if s.State != "not_answering" || s.URL != webUI || !strings.Contains(s.Message, "has not answered yet") {
		t.Errorf("final status = %+v; want not_answering with %s", s, webUI)
	}
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("finish did not return")
	}
	if answered {
		t.Error("finish reported the WebUI answered")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(probed) == 0 || probed[0] != "http://127.0.0.1:18790/health" {
		t.Errorf("probed %q; want the WebUI's health URL", probed)
	}
}

func TestProbeHealth(t *testing.T) {
	ok := httptest.NewServer(healthHandler(http.StatusOK))
	defer ok.Close()
	if err := probeHealth(context.Background(), ok.URL+"/health"); err != nil {
		t.Errorf("200: %v; want nil", err)
	}
	unavailable := httptest.NewServer(healthHandler(http.StatusServiceUnavailable))
	defer unavailable.Close()
	if err := probeHealth(context.Background(), unavailable.URL+"/health"); err == nil {
		t.Error("503 counted as answering")
	}

	// Something listening that is not the WebUI does not count.
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := ln.Close(); cerr != nil {
			t.Log(cerr)
		}
	}()
	go func() {
		for {
			c, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			if cerr := c.Close(); cerr != nil {
				t.Log(cerr)
			}
		}
	}()
	if err := probeHealth(context.Background(), "http://"+ln.Addr().String()+"/health"); err == nil {
		t.Error("a bare TCP listener counted as answering")
	}
}

func TestBrowserCommand(t *testing.T) {
	const code = "Alice_code-0123456789abcdefghijklmnopqrstuvwx"
	link := "http://127.0.0.1:1234/?code=" + code
	user := &TargetUser{Username: "alice", UID: "501"}
	root := &TargetUser{Username: "alice", UID: "501", IsRoot: true}
	for _, tc := range []struct {
		name       string
		goos       string
		tu         *TargetUser
		link       string
		wantArgv   []string
		wantScript string
	}{
		{"macOS user", "darwin", user, link, []string{"/usr/bin/osascript"}, `open location "` + link + `"` + "\n"},
		{
			"macOS root", "darwin", root, link,
			[]string{"/bin/launchctl", "asuser", "501", "/usr/bin/sudo", "-u", "alice", "/usr/bin/osascript"},
			`open location "` + link + `"` + "\n",
		},
		{
			"macOS escaping", "darwin", user, `http://127.0.0.1:1/?code=a"b\c`,
			[]string{"/usr/bin/osascript"},
			`open location "http://127.0.0.1:1/?code=a\"b\\c"` + "\n",
		},
		{"macOS root without an account", "darwin", &TargetUser{IsRoot: true}, link, nil, ""},
		{"Linux user", "linux", user, link, nil, ""},
		{"Linux root", "linux", root, link, nil, ""},
		{"other", "freebsd", user, link, nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			argv, script := browserCommand(tc.goos, tc.tu, tc.link)
			if !slices.Equal(argv, tc.wantArgv) || script != tc.wantScript {
				t.Errorf("browserCommand = %q, %q; want %q, %q", argv, script, tc.wantArgv, tc.wantScript)
			}
			for _, arg := range argv {
				if strings.Contains(arg, tc.link) || strings.Contains(arg, code) || strings.Contains(arg, "code=") {
					t.Errorf("argument %q carries the link", arg)
				}
			}
		})
	}
}

func TestWebUIAddress(t *testing.T) {
	gw := config.DefaultConfig().Gateway
	want := gw.LocalHTTPURL()
	for _, host := range []string{"", "127.0.0.1", "0.0.0.0", "192.0.2.10"} {
		gw.Host = host
		if got := webUIAddress(gw); got != want {
			t.Errorf("host %q: %q; want the loopback HTTP address %q", host, got, want)
		}
	}
}
