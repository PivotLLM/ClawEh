// ClawEh
// License: MIT

package gateway

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/tenebris-tech/alerter"

	"github.com/PivotLLM/ClawEh/app"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/internal/perms"
	"github.com/PivotLLM/ClawEh/logger"
)

// alertsFileName is the alerts log inside the data directory's logs folder,
// used unless ALERTER_LOG names another file.
const alertsFileName = "alerts.log"

// newAlerter builds the operator alerter for this gateway. Alerts go to
// <baseDir>/logs/alerts.log unless ALERTER_LOG is set, in which case the
// module's own precedence applies. A log that cannot be opened disables
// alerting (with a warning) rather than stopping the gateway. The path
// returned is the one alerts are written to, for the WebUI to read. The log
// file and any folders the alerter creates for it take exactly modes, the logs
// area's (security.permissions).
func newAlerter(baseDir string, modes config.Modes) (alerter.Alerter, string) {
	opts := []alerter.Option{
		alerter.WithAppName(app.Name()),
		alerter.WithInstanceName(shortHostname()),
		alerter.WithFileModes(modes.Dir, modes.File),
	}
	path := strings.TrimSpace(os.Getenv(alerter.EnvLogFile))
	if path == "" {
		path = filepath.Join(baseDir, "logs", alertsFileName)
		opts = append(opts, alerter.WithLogFile(path))
		// The alerter keeps an existing log's mode; one an earlier run left
		// at another mode is set to exactly the logs file mode here.
		if _, err := os.Lstat(path); err == nil {
			if err := perms.EnsureFileMode(path, modes.File); err != nil {
				logger.WarnCF("gateway", "Failed to set the alerts log mode", map[string]any{"path": path, "error": err.Error()})
			}
		}
	}
	a, err := alerter.New(opts...)
	if err != nil {
		logger.ErrorCF("gateway", "alerting disabled: could not open the alerts log",
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
