// ClawEh
// License: MIT

package gateway

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tenebris-tech/alerter"

	"github.com/PivotLLM/ClawEh/config"
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
	// Hermetic: no ~/.alerter from the developer's home, no ALERTER_* channel
	// from the shell, so nothing is delivered anywhere but the log.
	t.Setenv("HOME", t.TempDir())
	for _, kv := range os.Environ() {
		if k, _, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(k, "ALERTER_") {
			t.Setenv(k, "")
		}
	}
	t.Setenv(alerter.EnvLogFile, "")
	logModes := config.Modes{Dir: 0o700, File: 0o600}
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	a, path := newAlerter(base, logModes)
	if path != filepath.Join(base, "logs", alertsFileName) {
		t.Errorf("path = %q", path)
	}
	a.Normal("test", "default path")
	if err := a.Close(t.Context()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(b), " NORMAL ") || !strings.Contains(string(b), "ClawEh@") {
		t.Errorf("alerts file = %q, err = %v", b, err)
	}

	envPath := filepath.Join(base, "env-alerts.log")
	t.Setenv(alerter.EnvLogFile, envPath)
	a, path = newAlerter(base, logModes)
	if path != envPath {
		t.Errorf("ALERTER_LOG not honoured: path = %q", path)
	}
	if err := a.Close(t.Context()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	t.Setenv(alerter.EnvLogFile, "")
	notDir := filepath.Join(base, "not-a-dir")
	if err := os.WriteFile(notDir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	a, path = newAlerter(notDir, logModes)
	if _, ok := a.(alerter.Nop); !ok || path != "" {
		t.Errorf("unwritable default must disable alerting: %T %q", a, path)
	}
}
