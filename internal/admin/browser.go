// ClawEh
// License: MIT

package admin

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/PivotLLM/ClawEh/app"
	"github.com/PivotLLM/ClawEh/internal/clock"
	"github.com/PivotLLM/ClawEh/utils"
)

// BrowserSetupLimit is how long the one-time page waits for the account. It
// is kept short so the wait and the service start fit inside the 10 minutes
// the macOS Installer allows a package script.
const BrowserSetupLimit = 5 * time.Minute

const (
	// browserBodyLimit caps a request body to the page.
	browserBodyLimit = 64 << 10
	// finalStateGrace is how long Close keeps the page up for the browser
	// to fetch the final state.
	finalStateGrace = 10 * time.Second
	// codeBytes is the length of the one-time code before encoding.
	codeBytes = 32
	// shutdownGrace bounds the graceful shutdown that lets replies in
	// flight finish.
	shutdownGrace = 5 * time.Second
)

// ErrBrowserSetupTimedOut means nobody created the account on the one-time
// page within BrowserSetupLimit.
var ErrBrowserSetupTimedOut = fmt.Errorf("the admin account was not created within %d minutes", int(BrowserSetupLimit/time.Minute))

// ErrBrowserSetupUnreachable means the link to the page could neither be
// shown on a terminal nor opened in a browser, so nobody can use the page.
var ErrBrowserSetupUnreachable = errors.New("the page to create the admin account could not be opened in a browser, and the output is not a terminal to show its link")

// Status states reported to the page.
const (
	stateWaiting      = "waiting"       // no valid submission yet
	stateWorking      = "working"       // account being created, service starting
	stateStarted      = "started"       // the WebUI answers; the page goes there
	stateNotAnswering = "not_answering" // the WebUI did not answer in time
	stateFailed       = "failed"        // the write or the service start failed
)

// BrowserSetupOptions configures StartBrowserSetup.
type BrowserSetupOptions struct {
	// Clock times the waiting limit and the final grace; nil means the
	// system clock.
	Clock clock.Clock
	// Out is a terminal the link is printed on; nil means the link is
	// printed nowhere (it carries the one-time code, so it must not reach a
	// log).
	Out io.Writer
	// Open tries to show the link in a browser and reports whether it did;
	// nil means it is not tried.
	Open func(link string) bool
}

// BrowserSetup is a one-time page on 127.0.0.1 that asks for the admin
// account. It accepts one valid submission, hands it to Wait, and then shows
// the install's progress until Close.
type BrowserSetup struct {
	clk       clock.Clock
	code      string
	host      string // 127.0.0.1:<port>, the only Host accepted
	link      string
	srv       *http.Server
	limit     clock.Timer
	submitted chan account

	mu       sync.Mutex
	accepted bool
	closed   bool
	state    string
	message  string
	target   string // the WebUI address once known

	finalServed chan struct{}
	finalOnce   sync.Once
	closeOnce   sync.Once
}

type account struct{ username, password string }

// StartBrowserSetup serves the page on a free 127.0.0.1 port, prints the
// link to opts.Out and calls opts.Open with it. The waiting limit starts now.
// When the link was neither printed nor opened it shuts the page down and
// returns ErrBrowserSetupUnreachable.
func StartBrowserSetup(opts BrowserSetupOptions) (*BrowserSetup, error) {
	code, err := randomToken(codeBytes)
	if err != nil {
		return nil, fmt.Errorf("one-time code: %w", err)
	}
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen for the account page: %w", err)
	}
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		utils.CloseQuietly(ln)
		return nil, fmt.Errorf("listen for the account page: unexpected address %v", ln.Addr())
	}
	host := net.JoinHostPort("127.0.0.1", strconv.Itoa(addr.Port))
	clk := clock.Or(opts.Clock)
	b := &BrowserSetup{
		clk:         clk,
		code:        code,
		host:        host,
		link:        "http://" + host + "/?code=" + code,
		submitted:   make(chan account, 1),
		state:       stateWaiting,
		finalServed: make(chan struct{}),
	}
	b.srv = &http.Server{
		Handler:           b,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	go func() { _ = b.srv.Serve(ln) }() //nolint:errcheck // Serve always returns ErrServerClosed after shutdown
	b.limit = clk.NewTimer(BrowserSetupLimit)

	printed := false
	if opts.Out != nil {
		_, err := fmt.Fprintf(opts.Out, "\nOpen this page to create the admin account:\n%s\n\n", b.link)
		printed = err == nil
	}
	opened := opts.Open != nil && opts.Open(b.link)
	if !printed && !opened {
		b.limit.Stop()
		b.markClosed()
		b.shutdown()
		return nil, ErrBrowserSetupUnreachable
	}
	return b, nil
}

// URL is the page's link, one-time code included.
func (b *BrowserSetup) URL() string { return b.link }

// Wait blocks until the page receives a valid account, the limit passes
// (ErrBrowserSetupTimedOut) or ctx ends. On the last two the page is shut
// down. After a successful Wait the page keeps running until Close.
func (b *BrowserSetup) Wait(ctx context.Context) (username, password string, err error) {
	select {
	case a := <-b.submitted:
		b.limit.Stop()
		return a.username, a.password, nil
	case <-b.limit.C():
		return b.limitReached()
	case <-ctx.Done():
		b.limit.Stop()
		b.markClosed()
		b.shutdown()
		return "", "", fmt.Errorf("admin account setup cancelled: %w", ctx.Err())
	}
}

// limitReached closes the page once the limit has passed, unless a
// submission was accepted in the meantime: one that raced the limit still
// counts.
func (b *BrowserSetup) limitReached() (username, password string, err error) {
	b.markClosed()
	select {
	case a := <-b.submitted:
		b.reopen()
		return a.username, a.password, nil
	default:
	}
	b.shutdown()
	return "", "", ErrBrowserSetupTimedOut
}

// AccountCreated reports that the account was written and the service is
// being started.
func (b *BrowserSetup) AccountCreated() {
	b.set(stateWorking, "Starting "+app.ShortName()+"…", "")
}

// SaveFailed reports that the account could not be written. The page shows
// a fixed sentence; the detail belongs in the installer's output.
func (b *BrowserSetup) SaveFailed() {
	b.set(stateFailed, "The account could not be saved; run the installer again.", "")
}

// StartFailed reports that the service could not be started. The page
// shows a fixed sentence; the detail belongs in the installer's output.
func (b *BrowserSetup) StartFailed() {
	b.set(stateFailed, app.ShortName()+" could not be started; run the installer again.", "")
}

// Started sends the browser on to webUI.
func (b *BrowserSetup) Started(webUI string) {
	b.set(stateStarted, fmt.Sprintf("Opening %s…", app.ShortName()), webUI)
}

// NotAnswering says the WebUI has not answered and shows a link to it.
func (b *BrowserSetup) NotAnswering(webUI string) {
	b.set(stateNotAnswering, app.ShortName()+" has not answered yet; try this link in a moment.", webUI)
}

// Close keeps the page up until the browser has fetched a final state, or
// for finalStateGrace, or until ctx ends, and then shuts it down. It returns
// at once when the page is already down.
func (b *BrowserSetup) Close(ctx context.Context) {
	b.mu.Lock()
	closed := b.closed
	b.mu.Unlock()
	if !closed {
		select {
		case <-b.finalServed:
		case <-b.clk.After(finalStateGrace):
		case <-ctx.Done():
		}
	}
	b.markClosed()
	b.shutdownGracefully(ctx)
}

func (b *BrowserSetup) set(state, message, target string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.state, b.message, b.target = state, message, target
}

func (b *BrowserSetup) markClosed() {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
}

func (b *BrowserSetup) reopen() {
	b.mu.Lock()
	b.closed = false
	b.mu.Unlock()
}

// shutdown closes the page at once, cutting requests in flight: for the
// limit and a cancel, where nothing is left to tell the browser.
func (b *BrowserSetup) shutdown() {
	b.closeOnce.Do(func() { _ = b.srv.Close() }) //nolint:errcheck // the listener is ours; nothing to report
}

// shutdownGracefully lets replies in flight (the final state) finish, for
// at most shutdownGrace or until parent ends, and then closes the page.
func (b *BrowserSetup) shutdownGracefully(parent context.Context) {
	b.closeOnce.Do(func() {
		ctx, cancel := b.clk.WithTimeout(parent, shutdownGrace)
		defer cancel()
		if err := b.srv.Shutdown(ctx); err != nil {
			_ = b.srv.Close() //nolint:errcheck // already giving up on the graceful path
		}
	})
}

// ServeHTTP checks every request (Host, Origin, code) and serves the form,
// the progress view and the status.
func (b *BrowserSetup) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	nonce, err := randomToken(16)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'nonce-"+nonce+"'; script-src 'nonce-"+nonce+
		"'; connect-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")

	if r.Host != b.host {
		http.Error(w, http.StatusText(http.StatusMisdirectedRequest), http.StatusMisdirectedRequest)
		return
	}
	b.mu.Lock()
	closed := b.closed
	b.mu.Unlock()
	if closed {
		http.NotFound(w, r)
		return
	}

	switch {
	case r.URL.Path == "/" && r.Method == http.MethodGet:
		if !b.codeOK(r.URL.Query().Get("code")) {
			http.NotFound(w, r)
			return
		}
		b.servePage(w, nonce)
	case r.URL.Path == "/" && r.Method == http.MethodPost:
		b.serveSubmit(w, r, nonce)
	case r.URL.Path == "/status" && r.Method == http.MethodGet:
		if !b.codeOK(r.URL.Query().Get("code")) {
			http.NotFound(w, r)
			return
		}
		b.serveStatus(w)
	default:
		http.NotFound(w, r)
	}
}

func (b *BrowserSetup) codeOK(got string) bool {
	return subtle.ConstantTimeCompare([]byte(got), []byte(b.code)) == 1
}

// servePage shows the form before a valid submission and the progress view
// after it.
func (b *BrowserSetup) servePage(w http.ResponseWriter, nonce string) {
	b.mu.Lock()
	accepted, message := b.accepted, b.message
	b.mu.Unlock()
	if accepted {
		b.render(w, http.StatusOK, b.pageData(nonce, pageProgress, func(d *pageData) { d.Message = message }))
		return
	}
	b.render(w, http.StatusOK, b.pageData(nonce, pageForm, nil))
}

func (b *BrowserSetup) serveSubmit(w http.ResponseWriter, r *http.Request, nonce string) {
	if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+b.host {
		http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, browserBodyLimit)
	if err := r.ParseForm(); err != nil {
		if _, tooLarge := errors.AsType[*http.MaxBytesError](err); tooLarge {
			http.Error(w, http.StatusText(http.StatusRequestEntityTooLarge), http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	if !b.codeOK(r.PostForm.Get("code")) {
		http.NotFound(w, r)
		return
	}
	if b.isAccepted() {
		b.render(w, http.StatusConflict, b.pageData(nonce, pageUsed, nil))
		return
	}

	username := strings.TrimSpace(r.PostForm.Get("username"))
	password, repeat := r.PostForm.Get("password"), r.PostForm.Get("repeat")
	if problem := accountProblem(username, password, repeat); problem != "" {
		b.render(w, http.StatusBadRequest, b.pageData(nonce, pageForm, func(d *pageData) {
			d.Username, d.Problem = username, problem
		}))
		return
	}

	b.mu.Lock()
	if b.accepted || b.closed {
		used := b.accepted
		b.mu.Unlock()
		if used {
			b.render(w, http.StatusConflict, b.pageData(nonce, pageUsed, nil))
		} else {
			http.NotFound(w, r)
		}
		return
	}
	b.accepted = true
	b.state, b.message = stateWorking, "Creating the account…"
	b.submitted <- account{username: username, password: password} // buffered; only one is ever sent
	b.mu.Unlock()

	http.Redirect(w, r, "/?code="+url.QueryEscape(b.code), http.StatusSeeOther)
}

func (b *BrowserSetup) isAccepted() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.accepted
}

// statusReply is the body of GET /status.
type statusReply struct {
	State   string `json:"state"`
	Message string `json:"message,omitempty"`
	URL     string `json:"url,omitempty"`
}

func (b *BrowserSetup) serveStatus(w http.ResponseWriter) {
	b.mu.Lock()
	reply := statusReply{State: b.state, Message: b.message, URL: b.target}
	b.mu.Unlock()
	body, err := json.Marshal(reply)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write(body); err != nil {
		return // the browser went away; it polls again or the grace ends
	}
	switch reply.State {
	case stateStarted, stateNotAnswering, stateFailed:
		b.finalOnce.Do(func() { close(b.finalServed) })
	}
}

func (b *BrowserSetup) pageData(nonce, mode string, edit func(*pageData)) pageData {
	d := pageData{
		Mode:      mode,
		Nonce:     nonce,
		Code:      b.code,
		Name:      app.Name(),
		MinLength: MinPasswordLength,
	}
	if edit != nil {
		edit(&d)
	}
	return d
}

func (b *BrowserSetup) render(w http.ResponseWriter, status int, d pageData) {
	var buf bytes.Buffer
	if err := pageTemplate.Execute(&buf, d); err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if _, err := w.Write(buf.Bytes()); err != nil {
		return // the browser went away
	}
}

// accountProblem returns one sentence saying what is wrong with a
// submission, or "" when it is valid.
func accountProblem(username, password, repeat string) string {
	if err := ValidateUsername(username); err != nil {
		return sentence(err.Error())
	}
	if err := ValidatePassword(password); err != nil {
		return sentence(err.Error())
	}
	if password != repeat {
		return "Passwords do not match."
	}
	return ""
}

// sentence turns a lower-case Go error text into a sentence.
func sentence(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError {
		return s
	}
	s = string(unicode.ToUpper(r)) + s[n:]
	if !strings.HasSuffix(s, ".") {
		s += "."
	}
	return s
}

func randomToken(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
