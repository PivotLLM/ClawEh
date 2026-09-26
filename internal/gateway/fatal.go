package gateway

import (
	"errors"
	"os"
	"sync"
	"time"

	"github.com/tenebris-tech/alerter"

	"github.com/PivotLLM/ClawEh/logger"
)

// exitCodeServiceDied is the process exit status after a core service (the
// HTTP listener, the MCP host server or the agent loop) died after startup.
// Non-zero so systemd's Restart=on-failure starts the gateway again, and
// distinct from 1 (a startup or configuration error) so the journal tells
// the two apart.
const exitCodeServiceDied = 3

// fatalShutdownTimeout bounds the graceful shutdown a dead core service
// triggers. When it elapses the process exits with exitCodeServiceDied whether
// or not the shutdown finished, so a hung shutdown cannot leave the gateway
// half-dead.
const fatalShutdownTimeout = 20 * time.Second

// coreFailures is the alert raised when the core service with that name dies,
// keyed by the alert's EventID. Details carry the error, which names the
// address where there is one.
var coreFailures = map[string]alerter.Alert{
	"http": {
		Title:       "HTTP listener stopped",
		Description: "the WebUI, API, health endpoint and channel webhooks on it are down; the gateway is shutting down so the service manager can restart it",
		EventID:     "http",
	},
	"mcpserver": {
		Title:       "MCP host server stopped",
		Description: "external MCP clients and CLI providers lose the host tools; the gateway is shutting down so the service manager can restart it",
		EventID:     "mcpserver",
	},
	"agent-loop": {
		Title:       "Agent loop stopped",
		Description: "no inbound messages are processed; the gateway is shutting down so the service manager can restart it",
		EventID:     "agent-loop",
	},
}

// serviceDiedError is the error gatewayCmd returns when a core service died and
// the gateway shut itself down; exitCode maps it to exitCodeServiceDied.
type serviceDiedError struct {
	Service string // coreFailures key
	Err     error
}

func (e *serviceDiedError) Error() string {
	return coreFailures[e.Service].Title + ": " + e.Err.Error()
}

func (e *serviceDiedError) Unwrap() error { return e.Err }

// ExitCode is the process status this failure exits with.
func (e *serviceDiedError) ExitCode() int { return exitCodeServiceDied }

// exitCode is the process exit status for an error gatewayCmd returned: 1 for
// a startup or configuration error, the failure's own code when a core
// service died.
func exitCode(err error) int {
	if died, ok := errors.AsType[*serviceDiedError](err); ok {
		return died.ExitCode()
	}
	return 1
}

// fatalNotifier is the one path a dead core service takes: log at error, raise
// the alert, hand the failure to the main loop (which runs the same graceful
// shutdown as SIGTERM and returns it as the exit reason) and arm a timer that
// exits the process anyway when the shutdown hangs. The first failure wins;
// later ones are logged only.
type fatalNotifier struct {
	alerter alerter.Alerter
	exit    func(int)     // os.Exit in production; tests record the code
	timeout time.Duration // fatalShutdownTimeout in production
	once    sync.Once
	// failed receives the first failure; the main loop selects on it.
	failed chan *serviceDiedError
}

func newFatalNotifier(a alerter.Alerter) *fatalNotifier {
	return &fatalNotifier{
		alerter: a,
		exit:    os.Exit,
		timeout: fatalShutdownTimeout,
		failed:  make(chan *serviceDiedError, 1),
	}
}

// fatalService reports that the core service name (a coreFailures key) died
// with err. Safe on a nil notifier, which only logs.
func (f *fatalNotifier) fatalService(name string, err error) {
	fields := map[string]any{"service": name, "error": err.Error()}
	if f == nil {
		logger.ErrorCF("gateway", "core service stopped", fields)
		return
	}
	first := false
	f.once.Do(func() {
		first = true
		logger.ErrorCF("gateway", "core service stopped; shutting down so the service manager restarts the gateway", fields)
		a, ok := coreFailures[name]
		if !ok {
			a = alerter.Alert{Title: "Core service stopped", Description: name + " died; the gateway is shutting down so the service manager can restart it", EventID: name}
		}
		a.Details = err.Error()
		f.alerter.Send(a)
		f.failed <- &serviceDiedError{Service: name, Err: err}
		time.AfterFunc(f.timeout, func() {
			logger.ErrorCF("gateway", "graceful shutdown did not finish in time; exiting", map[string]any{
				"timeout": f.timeout.String(), "exit_code": exitCodeServiceDied,
			})
			f.exit(exitCodeServiceDied)
		})
	})
	if !first {
		logger.ErrorCF("gateway", "core service stopped during shutdown", fields)
	}
}

// handlerFor adapts fatalService to the func(error) callback shape the MCP
// host takes, for the service name. A nil notifier still logs.
func (f *fatalNotifier) handlerFor(name string) func(error) {
	return func(err error) { f.fatalService(name, err) }
}
