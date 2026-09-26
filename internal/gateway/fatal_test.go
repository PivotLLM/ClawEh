package gateway

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/internal/testalerts"
)

// newTestFatal builds a notifier whose exit is recorded, not performed, and
// whose hard-exit timer is short.
func newTestFatal(t *testing.T) (*fatalNotifier, *testalerts.Recorder, chan int) {
	t.Helper()
	rec := testalerts.Install(t)
	f := newFatalNotifier(rec)
	exits := make(chan int, 4)
	f.exit = func(code int) { exits <- code }
	f.timeout = 50 * time.Millisecond
	return f, rec, exits
}

// TestFatalService_FirstFailureAlertsSignalsAndExits: one dead service raises
// its alert with the existing title and event id, hands the failure to the
// main loop, and the hard-exit timer fires with exitCodeServiceDied.
func TestFatalService_FirstFailureAlertsSignalsAndExits(t *testing.T) {
	f, rec, exits := newTestFatal(t)
	f.fatalService("http", errors.New("127.0.0.1:18790: accept: boom"))

	select {
	case failure := <-f.failed:
		if failure.Service != "http" || !strings.Contains(failure.Err.Error(), "boom") {
			t.Fatalf("failure = %+v", failure)
		}
		if got := failure.Error(); !strings.HasPrefix(got, "HTTP listener stopped: ") {
			t.Fatalf("Error() = %q", got)
		}
	default:
		t.Fatal("no failure handed to the main loop")
	}

	alerts := rec.Alerts()
	if len(alerts) != 1 {
		t.Fatalf("alerts = %d, want 1", len(alerts))
	}
	if alerts[0].Title != "HTTP listener stopped" || alerts[0].EventID != "http" || !strings.Contains(alerts[0].Details, "boom") {
		t.Fatalf("alert = %+v", alerts[0])
	}

	select {
	case code := <-exits:
		if code != exitCodeServiceDied {
			t.Fatalf("exit code = %d, want %d", code, exitCodeServiceDied)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("hard-exit timer did not fire")
	}
}

// TestFatalService_SecondFailureIsLoggedOnly: a second service dying during
// the shutdown neither alerts again nor arms a second timer.
func TestFatalService_SecondFailureIsLoggedOnly(t *testing.T) {
	f, rec, exits := newTestFatal(t)
	f.fatalService("mcpserver", errors.New("127.0.0.1:18789: boom"))
	f.fatalService("agent-loop", errors.New("loop gone"))

	<-f.failed
	select {
	case failure := <-f.failed:
		t.Fatalf("second failure handed to the main loop: %+v", failure)
	default:
	}
	if got := rec.Alerts(); len(got) != 1 || got[0].Title != "MCP host server stopped" {
		t.Fatalf("alerts = %+v, want the one MCP alert", got)
	}
	<-exits
	select {
	case code := <-exits:
		t.Fatalf("second exit(%d)", code)
	case <-time.After(150 * time.Millisecond):
	}
}

// TestFatalService_EveryCoreServiceHasAnAlert pins the table: the three
// services that take the process down each keep their alert title and id.
func TestFatalService_EveryCoreServiceHasAnAlert(t *testing.T) {
	for name, title := range map[string]string{
		"http":       "HTTP listener stopped",
		"mcpserver":  "MCP host server stopped",
		"agent-loop": "Agent loop stopped",
	} {
		a, ok := coreFailures[name]
		if !ok || a.Title != title || a.EventID != name {
			t.Errorf("coreFailures[%q] = %+v, want title %q and event id %q", name, a, title, name)
		}
	}
}

// TestFatalService_NilNotifier: call sites built without a gateway (tests,
// other embedders) log and carry on.
func TestFatalService_NilNotifier(t *testing.T) {
	var f *fatalNotifier
	f.fatalService("http", errors.New("boom"))
	f.handlerFor("mcpserver")(errors.New("boom"))
}

// TestExitCode maps the gateway's exit reasons onto process statuses.
func TestExitCode(t *testing.T) {
	sf := &serviceDiedError{Service: "http", Err: errors.New("boom")}
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"service died", sf, exitCodeServiceDied},
		{"wrapped service died", fmt.Errorf("gateway: %w", sf), exitCodeServiceDied},
		{"startup error", errors.New("error loading config"), 1},
	} {
		if got := exitCode(tc.err); got != tc.want {
			t.Errorf("%s: exitCode = %d, want %d", tc.name, got, tc.want)
		}
	}
	if !errors.Is(fmt.Errorf("x: %w", sf), sf.Err) {
		t.Error("serviceDiedError does not unwrap to its cause")
	}
}

// TestHTTPHostServeErrorReportsFatal: a listener that dies after Start is
// handed to onFatal as "http" with its address; a closed one before Serve is
// the simplest way to make Serve fail with something other than ErrServerClosed.
func TestHTTPHostServeErrorReportsFatal(t *testing.T) {
	h, err := newHTTPHost(hostOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var gotName string
	var gotErr error
	h.onFatal = func(name string, err error) { gotName, gotErr = name, err }

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	h.serve(&http.Server{Handler: h.handler, ReadHeaderTimeout: time.Second}, ln, false)

	if gotName != "http" || gotErr == nil || !strings.Contains(gotErr.Error(), addr) {
		t.Fatalf("onFatal(%q, %v), want (\"http\", error naming %s)", gotName, gotErr, addr)
	}
}
