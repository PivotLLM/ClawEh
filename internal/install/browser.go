package install

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/internal"
	"github.com/PivotLLM/ClawEh/internal/admin"
	"github.com/PivotLLM/ClawEh/internal/clock"
	"github.com/PivotLLM/ClawEh/logger"
)

const (
	// webUIWait is how long --browser-setup waits for the WebUI to accept a
	// connection after the service was started.
	webUIWait = 60 * time.Second
	// webUIPoll is the pause between connection attempts.
	webUIPoll = 500 * time.Millisecond
	// openerWait bounds the command that opens the browser; osascript
	// hands the link on and returns at once.
	openerWait = 10 * time.Second
	// probeWait bounds one health check of the WebUI.
	probeWait = 2 * time.Second
)

// browserSetup is what `install --browser-setup` uses; a nil *browserSetup
// means the flag is off. Tests build their own.
type browserSetup struct {
	// clock times the page's limit and the wait for the WebUI; nil means
	// the system clock.
	clock clock.Clock
	// terminal is the installer's output when it is a terminal, else nil:
	// the link carries the one-time code, so it is never printed to a log.
	terminal io.Writer
	// open opens link in tu's browser and reports whether it did.
	open func(ctx context.Context, tu *TargetUser, link string) bool
	// probe checks the WebUI's health URL and returns nil when it answers.
	probe func(ctx context.Context, healthURL string) error
	// interrupts returns a context Ctrl+C cancels while the page waits for
	// the account; its stop ends the interception.
	interrupts func(parent context.Context) (context.Context, context.CancelFunc)
}

// newBrowserSetup returns the real --browser-setup wiring.
func newBrowserSetup() *browserSetup {
	var terminal io.Writer
	if term.IsTerminal(int(os.Stdout.Fd())) {
		terminal = os.Stdout
	}
	return &browserSetup{
		terminal: terminal,
		open:     openInBrowser,
		probe:    probeHealth,
		interrupts: func(parent context.Context) (context.Context, context.CancelFunc) {
			return signal.NotifyContext(parent, os.Interrupt)
		},
	}
}

// ensureAccount runs admin.EnsureAccount with the one-time page in place of
// the terminal prompt. Ctrl+C is intercepted only until it returns. page is
// the page once it has received the
// account (nil when it never did, or was not needed); the caller reports the
// rest of the install on it and closes it.
func (bs *browserSetup) ensureAccount(opts admin.EnsureOptions, tu *TargetUser) (username string, created bool, page *admin.BrowserSetup, err error) {
	ctx, stop := bs.interrupts(context.Background())
	defer stop()

	opts.Ask = func() (string, string, error) {
		p, startErr := admin.StartBrowserSetup(admin.BrowserSetupOptions{
			Clock: bs.clock,
			Out:   bs.terminal,
			Open:  func(link string) bool { return bs.open(ctx, tu, link) },
		})
		if startErr != nil {
			return "", "", startErr
		}
		user, password, waitErr := p.Wait(ctx)
		if waitErr != nil {
			return "", "", waitErr
		}
		page = p
		return user, password, nil
	}
	username, created, err = admin.EnsureAccount(opts)
	return username, created, page, err
}

// finish waits for the WebUI to answer and sends the page there, or shows a
// link when it does not answer in time, then closes the page. It reports
// whether the WebUI answered.
func (bs *browserSetup) finish(ctx context.Context, page *admin.BrowserSetup, clawHome string) bool {
	address := webUIAddress(installedGateway(clawHome))
	answered := waitForWebUI(ctx, bs.clock, bs.probe, address, webUIWait)
	if answered {
		page.Started(address)
	} else {
		page.NotAnswering(address)
	}
	page.Close(ctx)
	return answered
}

// waitForWebUI reports whether the WebUI at address answers its health
// check, asked with probe, within wait on clk (nil means the system clock).
func waitForWebUI(ctx context.Context, clk clock.Clock, probe func(context.Context, string) error, address string, wait time.Duration) bool {
	healthURL, err := url.JoinPath(address, "health")
	if err != nil {
		return false
	}
	clk = clock.Or(clk)
	waitCtx, cancel := clk.WithTimeout(ctx, wait)
	defer cancel()
	for {
		// Probe on ctx, not waitCtx: a test clock's deadline is not wall time.
		if probe(ctx, healthURL) == nil {
			return true
		}
		select {
		case <-waitCtx.Done():
			return false
		case <-clk.After(webUIPoll):
		}
	}
}

// probeHealth GETs healthURL, the WebUI's /health (exempt from the login,
// 200 as soon as the listener serves), and expects 200.
func probeHealth(ctx context.Context, healthURL string) error {
	ctx, cancel := context.WithTimeout(ctx, probeWait)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10)) //nolint:errcheck // drained only to reuse the connection
	if err := resp.Body.Close(); err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health check: %s", resp.Status)
	}
	return nil
}

// openInBrowser opens link in tu's default browser and waits for the
// opener, bounded by openerWait. A start failure, a non-zero exit or the
// bound passing means the browser was not opened.
func openInBrowser(ctx context.Context, tu *TargetUser, link string) bool {
	argv, script := browserCommand(runtime.GOOS, tu, link)
	if argv == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, openerWait)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // fixed opener binaries and fixed arguments; the link goes on standard input
	cmd.Stdin = strings.NewReader(script)
	if err := cmd.Run(); err != nil {
		logger.DebugCF("install", "could not open the browser", map[string]any{"error": err.Error()})
		return false
	}
	return true
}

// browserCommand is the command that opens link in tu's default browser and
// the script it reads on standard input, or nil when none is tried. On macOS
// it is osascript running `open location`, in the target user's session as
// that user when the installer runs as root. The link (it carries the
// one-time code) is never an argument, so it stays out of the process list
// and the sudo log. Elsewhere nothing is opened; the link is shown on the
// terminal.
func browserCommand(goos string, tu *TargetUser, link string) (argv []string, script string) {
	if goos != "darwin" {
		return nil, ""
	}
	script = "open location " + appleScriptString(link) + "\n"
	if tu != nil && tu.IsRoot {
		if tu.UID == "" || tu.Username == "" {
			return nil, ""
		}
		return []string{"/bin/launchctl", "asuser", tu.UID, "/usr/bin/sudo", "-u", tu.Username, "/usr/bin/osascript"}, script
	}
	return []string{"/usr/bin/osascript"}, script
}

// appleScriptString quotes s as an AppleScript string literal.
func appleScriptString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// installedGateway is the gateway config of the install, or the defaults
// when config.json cannot be read (a fresh install has none yet; another
// account on the Mac cannot read the data folder). Only the gateway block is
// read, so a secret reference the caller's environment cannot resolve does
// not hide the configured port.
func installedGateway(clawHome string) config.GatewayConfig {
	cfgPath := internal.GetConfigPath()
	if clawHome != "" {
		cfgPath = filepath.Join(clawHome, "config.json")
	}
	gw, err := config.ReadGateway(cfgPath)
	if err != nil {
		return config.DefaultConfig().Gateway
	}
	return gw
}

// webUIAddress is where the browser, on this machine, goes once the service
// runs: plain HTTP on loopback, which is always bound whatever gateway.host
// says (GatewayConfig.HTTPBindHosts).
func webUIAddress(gw config.GatewayConfig) string {
	return gw.LocalHTTPURL()
}
