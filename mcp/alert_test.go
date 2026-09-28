// ClawEh
// License: MIT

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/server"
	"github.com/tenebris-tech/alerter"

	"github.com/PivotLLM/ClawEh/config"
)

type alertRecorder struct {
	mu     sync.Mutex
	alerts []alerter.Alert
}

func (r *alertRecorder) Send(a alerter.Alert) {
	r.mu.Lock()
	r.alerts = append(r.alerts, a)
	r.mu.Unlock()
}
func (r *alertRecorder) Normal(string, string, ...string)    {}
func (r *alertRecorder) Urgent(string, string, ...string)    {}
func (r *alertRecorder) Emergency(string, string, ...string) {}
func (r *alertRecorder) Close(context.Context) error         { return nil }

func (r *alertRecorder) take() []alerter.Alert {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.alerts
	r.alerts = nil
	return out
}

// fakeClock is a settable clock for the cadence and backoff tests.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// newAlertingManager returns a manager with a recorder and a fake clock.
func newAlertingManager(t *testing.T) (*Manager, *alertRecorder, *fakeClock) {
	t.Helper()
	mgr := NewManager()
	clock := &fakeClock{t: time.Date(2026, 9, 27, 14, 5, 0, 0, time.UTC)}
	mgr.nowFn = clock.now
	rec := &alertRecorder{}
	mgr.SetAlerter(rec)
	t.Cleanup(func() {
		if err := mgr.Close(context.Background()); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return mgr, rec, clock
}

// TestRetryDisconnected_AlertsUnreachable: a desired server that cannot be
// connected raises one Normal alert keyed by its name.
func TestRetryDisconnected_AlertsUnreachable(t *testing.T) {
	mgr := NewManager()
	defer func() {
		if err := mgr.Close(context.Background()); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()
	rec := &alertRecorder{}
	mgr.SetAlerter(rec)

	down := httptest.NewServer(server.NewStreamableHTTPServer(server.NewMCPServer("down", "0.0.1")))
	downURL := down.URL
	down.Close()
	mgr.setDesired(map[string]config.MCPServerConfig{
		"down": {Enabled: true, Type: "http", URL: downURL},
	})
	mgr.RetryDisconnected(context.Background())

	got := rec.take()
	if len(got) != 1 || got[0].Priority != alerter.Normal || got[0].EventID != "down" ||
		got[0].Title != "MCP down down" {
		t.Fatalf("expected one Normal alert for 'down', got %+v", got)
	}
}

// TestAlertCadence: the first failure alerts, repeats within the hour do not,
// a failure after 61 minutes reminds, and recovery alerts once.
func TestAlertCadence(t *testing.T) {
	mgr, rec, clock := newAlertingManager(t)
	fail := errors.New("failed to connect: transport error: transport closed")

	mgr.recordFailure("www", fail)
	got := rec.take()
	if len(got) != 1 || got[0].Title != "MCP www down" || got[0].EventID != "www" {
		t.Fatalf("first failure: want one down alert, got %+v", got)
	}
	if got[0].Description != fail.Error() {
		t.Fatalf("down description = %q, want the error", got[0].Description)
	}

	clock.advance(30 * time.Second)
	mgr.recordFailure("www", fail)
	if got = rec.take(); len(got) != 0 {
		t.Fatalf("immediate second failure must not alert, got %+v", got)
	}

	clock.advance(61 * time.Minute)
	mgr.recordFailure("www", fail)
	got = rec.take()
	if len(got) != 1 || got[0].Title != "MCP www down" ||
		!strings.HasPrefix(got[0].Description, "still down since 14:05: ") {
		t.Fatalf("failure after 61 minutes: want one reminder, got %+v", got)
	}

	clock.advance(time.Minute)
	mgr.recordFailure("www", fail)
	if got = rec.take(); len(got) != 0 {
		t.Fatalf("failure right after a reminder must not alert, got %+v", got)
	}

	mgr.recordSuccess("www")
	got = rec.take()
	if len(got) != 1 || got[0].Title != "MCP www up" || got[0].EventID != "www-up" ||
		got[0].Description != "reconnected after 1h2m" {
		t.Fatalf("recovery: want one up alert, got %+v", got)
	}
	if msg, _ := mgr.lastError("www"); msg != "" {
		t.Fatalf("last error must clear on recovery, got %q", msg)
	}

	mgr.recordSuccess("www")
	if got := rec.take(); len(got) != 0 {
		t.Fatalf("a success while up must not alert, got %+v", got)
	}
}

// TestAlertCadence_SurvivesSync: a reload that retries a server already
// reported down does not alert it again.
func TestAlertCadence_SurvivesSync(t *testing.T) {
	mgr, rec, _ := newAlertingManager(t)
	cfg := config.MCPConfig{Servers: map[string]config.MCPServerConfig{
		"dead": {Enabled: true, Type: "http", URL: "http://127.0.0.1:1/mcp"},
	}}
	ctx := context.Background()

	if err := mgr.Sync(ctx, cfg, ""); err == nil {
		t.Fatal("expected Sync to a dead address to fail")
	}
	if got := rec.take(); len(got) != 1 {
		t.Fatalf("first Sync: want one alert, got %+v", got)
	}
	if err := mgr.Sync(ctx, cfg, ""); err == nil {
		t.Fatal("expected Sync to a dead address to fail")
	}
	if got := rec.take(); len(got) != 0 {
		t.Fatalf("reload must not re-alert a server already down, got %+v", got)
	}
}

// TestDownAlertText: the alert fits an SMS, names the server first, prefers
// the child's own message, and keeps the full error and stderr in details.
func TestDownAlertText(t *testing.T) {
	tail := &stderrTail{}
	if _, err := tail.Write([]byte("starting\n\nError: config file /etc/www/" + strings.Repeat("x", 200) + ".json not found\n")); err != nil {
		t.Fatal(err)
	}
	err := withStderr(errors.New("failed to connect: transport error: transport closed"), tail)

	a := downAlert("www", err, "")
	if a.Title != "MCP www down" {
		t.Fatalf("title = %q", a.Title)
	}
	if n := len([]rune(a.Description)); n > alertReasonMax {
		t.Fatalf("description is %d runes, want <= %d", n, alertReasonMax)
	}
	if !strings.HasPrefix(a.Description, "Error: config file /etc/www/") || strings.Contains(a.Description, "\n") {
		t.Fatalf("description should be the child's last line on one line, got %q", a.Description)
	}
	for _, want := range []string{"transport closed; stderr: Error: config file", "stderr:\nstarting\nError:", "MCP servers page"} {
		if !strings.Contains(a.Details, want) {
			t.Fatalf("details missing %q:\n%s", want, a.Details)
		}
	}

	reminder := downAlert("www", err, "still down since 14:05: ")
	if !strings.HasPrefix(reminder.Description, "still down since 14:05: Error:") ||
		len([]rune(reminder.Description)) > alertReasonMax {
		t.Fatalf("reminder description = %q", reminder.Description)
	}
}

// TestStderrTail_Bounds keeps only the last bytes and lines.
func TestStderrTail_Bounds(t *testing.T) {
	tail := &stderrTail{}
	for i := range 3000 {
		if _, err := tail.Write([]byte("line " + strings.Repeat("y", i%7) + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	lines := tail.Lines()
	if len(lines) != stderrTailLines {
		t.Fatalf("got %d lines, want %d", len(lines), stderrTailLines)
	}
	if tail.last() != lines[len(lines)-1] || !strings.HasPrefix(tail.last(), "line ") {
		t.Fatalf("last line = %q", tail.last())
	}
	if len(tail.buf) > stderrTailBytes {
		t.Fatalf("buffer holds %d bytes, want <= %d", len(tail.buf), stderrTailBytes)
	}
}

// TestStdioFailure_CapturesStderr: a stdio child that prints to stderr and
// exits surfaces its message in the connect error, the alert and Status.
func TestStdioFailure_CapturesStderr(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	mgr, rec, _ := newAlertingManager(t)
	cfg := config.MCPServerConfig{
		Enabled: true,
		Command: "sh",
		Args:    []string{"-c", "echo 'booting' >&2; echo 'Error: config file /tmp/missing.json not found' >&2; exit 1"},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	err := mgr.ConnectServer(ctx, "www", cfg)
	if err == nil {
		t.Fatal("expected the connect to fail")
	}
	if !strings.HasSuffix(err.Error(), "; stderr: Error: config file /tmp/missing.json not found") {
		t.Fatalf("error should end with the child's last stderr line, got %q", err)
	}

	got := rec.take()
	if len(got) != 1 || got[0].Description != "Error: config file /tmp/missing.json not found" {
		t.Fatalf("want one alert with the child's message, got %+v", got)
	}

	var st *ServerStatus
	for _, s := range mgr.Status() {
		if s.Name == "www" {
			st = &s
		}
	}
	if st == nil || st.LastError != err.Error() || st.LastErrorAt.IsZero() {
		t.Fatalf("status should carry the last error, got %+v", st)
	}
	raw, jerr := json.Marshal(st)
	if jerr != nil {
		t.Fatal(jerr)
	}
	if !strings.Contains(string(raw), `"last_error":"failed to`) || !strings.Contains(string(raw), `"last_error_at":"`) {
		t.Fatalf("status JSON missing last_error fields: %s", raw)
	}
}

// TestStatus_LastErrorEmptyWhenConnected: a connected server reports no error.
func TestStatus_LastErrorEmptyWhenConnected(t *testing.T) {
	good := newTestMCPServer(t)
	mgr, _, _ := newAlertingManager(t)
	mgr.recordFailure("live", errors.New("earlier failure"))
	if err := mgr.ConnectServer(context.Background(), "live", config.MCPServerConfig{
		Enabled: true, Type: "http", URL: good.URL,
	}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	st := mgr.Status()
	if len(st) != 1 || st[0].State != StateConnected || st[0].LastError != "" || !st[0].LastErrorAt.IsZero() {
		t.Fatalf("connected status should have no error, got %+v", st)
	}
	raw, err := json.Marshal(st[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"last_error":""`) || strings.Contains(string(raw), "last_error_at") {
		t.Fatalf("connected status JSON = %s", raw)
	}
}

// TestBackoff doubles the wait per failure from the initial cooldown up to
// ten minutes, and a success resets it.
func TestBackoff(t *testing.T) {
	mgr, _, clock := newAlertingManager(t)
	fail := errors.New("connection refused")
	wantWaits := []time.Duration{
		30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute,
		8 * time.Minute, 10 * time.Minute, 10 * time.Minute,
	}
	for i, want := range wantWaits {
		mgr.recordFailure("svc", fail)
		until, ok := mgr.reconnectCooldownUntil("svc")
		if !ok || until.Sub(clock.now()) != want {
			t.Fatalf("failure %d: wait = %v, want %v", i+1, until.Sub(clock.now()), want)
		}
		clock.advance(want)
	}

	mgr.recordSuccess("svc")
	mgr.recordFailure("svc", fail)
	if until, _ := mgr.reconnectCooldownUntil("svc"); until.Sub(clock.now()) != 30*time.Second {
		t.Fatalf("after a success the wait must reset to 30s, got %v", until.Sub(clock.now()))
	}

	if got := backoff(15*time.Minute, 4); got != 15*time.Minute {
		t.Fatalf("an initial cooldown above the cap must be kept, got %v", got)
	}
}

// TestReconnect_BypassesBackoff: the explicit Reconnect (the MCP servers
// page's button) runs at once for a server deep in backoff, while the
// background retry still waits.
func TestReconnect_BypassesBackoff(t *testing.T) {
	good := newTestMCPServer(t)
	mgr, rec, _ := newAlertingManager(t)
	mgr.setDesired(map[string]config.MCPServerConfig{
		"svc": {Enabled: true, Type: "http", URL: good.URL},
	})
	for range 5 {
		mgr.recordFailure("svc", errors.New("connection refused"))
	}
	rec.take()

	if got := mgr.RetryDisconnected(context.Background()); len(got) != 0 {
		t.Fatalf("background retry must wait out the backoff, connected %v", got)
	}
	if err := mgr.Reconnect(context.Background(), "svc"); err != nil {
		t.Fatalf("Reconnect: %v", err)
	}
	if _, ok := mgr.GetServer("svc"); !ok {
		t.Fatal("svc should be connected")
	}
	if got := rec.take(); len(got) != 1 || got[0].Title != "MCP svc up" {
		t.Fatalf("want one recovery alert, got %+v", got)
	}
}
