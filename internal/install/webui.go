package install

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/PivotLLM/ClawEh/app"
	"github.com/PivotLLM/ClawEh/internal/clock"
	"github.com/PivotLLM/ClawEh/logger"
)

const (
	// launcherWait is how long `webui` waits for the WebUI to answer.
	launcherWait = 10 * time.Second
	// dialogGivesUp is how long the "not running" dialog stays up.
	dialogGivesUp = 120 * time.Second
	// dialogWait bounds the command showing that dialog, a little past the
	// dialog's own limit.
	dialogWait = dialogGivesUp + 5*time.Second
)

// webUILauncher is what `webui` uses. Tests build their own.
type webUILauncher struct {
	// clock times the wait for the WebUI; nil means the system clock.
	clock clock.Clock
	// probe checks the WebUI's health URL and returns nil when it answers.
	probe func(ctx context.Context, healthURL string) error
	// open opens address in the default browser and reports whether it did.
	open func(ctx context.Context, address string) bool
	// dialog runs an AppleScript that shows a dialog.
	dialog func(ctx context.Context, script string) error
	// stderrIsTerminal says whether standard error is a terminal; when it is
	// not (the app was clicked), a problem is also shown in a dialog.
	stderrIsTerminal bool
}

// NewWebUICommand returns the `webui` subcommand, which the macOS app runs.
func NewWebUICommand() *cobra.Command {
	return &cobra.Command{
		Use:          "webui",
		Short:        "Open the WebUI in your browser",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return newWebUILauncher().run(cmd.Context())
		},
	}
}

// newWebUILauncher returns the real `webui` wiring.
func newWebUILauncher() *webUILauncher {
	return &webUILauncher{
		probe:            probeHealth,
		open:             func(ctx context.Context, address string) bool { return openInBrowser(ctx, nil, address) },
		dialog:           runDialog,
		stderrIsTerminal: term.IsTerminal(int(os.Stderr.Fd())),
	}
}

// run waits for the WebUI and opens it in the browser, or says it is not
// running.
func (l *webUILauncher) run(ctx context.Context) error {
	address := webUIAddress(installedGateway(""))
	if !waitForWebUI(ctx, l.clock, l.probe, address, launcherWait) {
		message := app.ShortName() + " is not running."
		if !l.stderrIsTerminal {
			if err := l.dialog(ctx, notRunningDialog(message)); err != nil {
				logger.DebugCF("install", "could not show the dialog", map[string]any{"error": err.Error()})
			}
		}
		return errors.New(message)
	}
	if !l.open(ctx, address) {
		return fmt.Errorf("could not open the browser; go to %s", address)
	}
	return nil
}

// notRunningDialog is the AppleScript that shows message in a dialog.
func notRunningDialog(message string) string {
	return "display dialog " + appleScriptString(message) +
		" with title " + appleScriptString(app.Name()) +
		` buttons {"OK"} default button "OK" with icon note giving up after ` +
		strconv.Itoa(int(dialogGivesUp/time.Second)) + "\n"
}

// runDialog runs script with osascript, on standard input, bounded by
// dialogWait.
func runDialog(ctx context.Context, script string) error {
	ctx, cancel := context.WithTimeout(ctx, dialogWait)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/osascript")
	cmd.Stdin = strings.NewReader(script)
	return cmd.Run()
}
