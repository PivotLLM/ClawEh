// ClawEh
// License: MIT

package gateway

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tenebris-tech/alerter"
)

func TestShortHostname(t *testing.T) {
	h := shortHostname()
	if strings.Contains(h, ".") {
		t.Errorf("shortHostname = %q, must stop at the first dot", h)
	}
}

// TestNewAlerter: alerts land in <base>/logs/alerts.log by default; ALERTER_LOG
// takes over when set; an unwritable default disables alerting quietly.
func TestNewAlerter(t *testing.T) {
	t.Setenv(alerter.EnvLogFile, "")
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	a, path := newAlerter(base)
	if path != filepath.Join(base, "logs", alertsFileName) {
		t.Errorf("path = %q", path)
	}
	a.High("test", "default path")
	if err := a.Close(t.Context()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(b), "HIGH ClawEh") {
		t.Errorf("alerts file = %q, err = %v", b, err)
	}

	envPath := filepath.Join(base, "env-alerts.log")
	t.Setenv(alerter.EnvLogFile, envPath)
	a, path = newAlerter(base)
	if path != envPath {
		t.Errorf("ALERTER_LOG not honoured: path = %q", path)
	}
	if err := a.Close(t.Context()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	t.Setenv(alerter.EnvLogFile, "")
	a, path = newAlerter(filepath.Join(base, "missing"))
	if _, ok := a.(alerter.Nop); !ok || path != "" {
		t.Errorf("unwritable default must disable alerting: %T %q", a, path)
	}
}
