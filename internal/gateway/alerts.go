// ClawEh
// License: MIT

package gateway

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/tenebris-tech/alerter"

	"github.com/PivotLLM/ClawEh/app"
	"github.com/PivotLLM/ClawEh/logger"
)

// alertsFileName is the alerts log inside the data directory's logs folder,
// used unless ALERTER_LOG names another file.
const alertsFileName = "alerts.txt"

// newAlerter builds the operator alerter for this gateway. Alerts go to
// <baseDir>/logs/alerts.txt unless ALERTER_LOG is set, in which case the
// module's own precedence applies. A log that cannot be opened disables
// alerting (with a warning) rather than stopping the gateway. The path
// returned is the one alerts are written to, for the WebUI to read.
func newAlerter(baseDir string) (alerter.Alerter, string) {
	opts := []alerter.Option{
		alerter.WithAppName(app.Name()),
		alerter.WithInstanceName(shortHostname()),
	}
	path := strings.TrimSpace(os.Getenv(alerter.EnvLogFile))
	if path == "" {
		path = filepath.Join(baseDir, "logs", alertsFileName)
		opts = append(opts, alerter.WithLogFile(path))
	}
	a, err := alerter.New(opts...)
	if err != nil {
		logger.WarnCF("gateway", "alerting disabled: could not open the alerts log",
			map[string]any{"path": path, "error": err.Error()})
		return alerter.Nop{}, ""
	}
	return a, path
}

// shortHostname is the host name up to the first dot, or "" when unknown.
func shortHostname() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	if i := strings.IndexByte(h, '.'); i > 0 {
		h = h[:i]
	}
	return h
}
