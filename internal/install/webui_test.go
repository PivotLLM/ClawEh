package install

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/app"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/internal/clock"
)

// launcherEnv is a `webui` wiring for tests that records what it was asked
// to do.
type launcherEnv struct {
	launcher *webUILauncher

	mu      sync.Mutex
	opened  []string
	scripts []string
}

func newLauncherEnv(clk clock.Clock, answers, stderrIsTerminal bool) *launcherEnv {
	e := &launcherEnv{}
	e.launcher = &webUILauncher{
		clock: clk,
		probe: func(context.Context, string) error {
			if answers {
				return nil
			}
			return errors.New("connection refused")
		},
		open: func(_ context.Context, address string) bool {
			e.mu.Lock()
			defer e.mu.Unlock()
			e.opened = append(e.opened, address)
			return true
		},
		dialog: func(_ context.Context, script string) error {
			e.mu.Lock()
			defer e.mu.Unlock()
			e.scripts = append(e.scripts, script)
			return nil
		},
		stderrIsTerminal: stderrIsTerminal,
	}
	return e
}

func (e *launcherEnv) record() (opened, scripts []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.opened...), append([]string(nil), e.scripts...)
}

func TestWebUILauncher_OpensConfiguredAddress(t *testing.T) {
	clawHome := t.TempDir()
	t.Setenv("CLAW_HOME", clawHome)
	address := writeGatewayPort(t, clawHome, 18999)
	env := newLauncherEnv(nil, true, false)

	if err := env.launcher.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	opened, scripts := env.record()
	if len(opened) != 1 || opened[0] != address {
		t.Errorf("opened %q; want once, %q", opened, address)
	}
	if len(scripts) != 0 {
		t.Errorf("dialog shown: %q", scripts)
	}
}

func TestWebUILauncher_OpenerFails(t *testing.T) {
	clawHome := t.TempDir()
	t.Setenv("CLAW_HOME", clawHome)
	address := writeGatewayPort(t, clawHome, 18999)
	env := newLauncherEnv(nil, true, false)
	env.launcher.open = func(context.Context, string) bool { return false }

	err := env.launcher.run(context.Background())
	if err == nil || err.Error() != "could not open the browser; go to "+address {
		t.Errorf("run = %v; want the address in the error", err)
	}
}

func TestWebUILauncher_NotRunning(t *testing.T) {
	for _, tc := range []struct {
		name       string
		terminal   bool
		wantDialog bool
	}{
		{"clicked", false, true},
		{"terminal", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CLAW_HOME", t.TempDir())
			clk := clock.NewFake(time.Unix(1_800_000_000, 0))
			env := newLauncherEnv(clk, false, tc.terminal)

			done := make(chan error, 1)
			go func() { done <- env.launcher.run(context.Background()) }()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := clk.BlockUntil(ctx, 2); err != nil { // the wait's deadline and its first pause
				t.Fatal(err)
			}
			clk.Advance(launcherWait - time.Millisecond)
			if err := clk.BlockUntil(ctx, 2); err != nil { // the deadline and the next pause
				t.Fatal(err)
			}
			select {
			case err := <-done:
				t.Fatalf("run returned %v before launcherWait", err)
			default:
			}
			clk.Advance(webUIPoll)
			var err error
			select {
			case err = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("run did not return")
			}

			want := app.ShortName() + " is not running."
			if err == nil || err.Error() != want {
				t.Errorf("run = %v; want %q", err, want)
			}
			opened, scripts := env.record()
			if len(opened) != 0 {
				t.Errorf("opened %q although nothing answered", opened)
			}
			if tc.wantDialog {
				if len(scripts) != 1 || scripts[0] != notRunningDialog(want) {
					t.Errorf("dialogs %q; want one, %q", scripts, notRunningDialog(want))
				}
			} else if len(scripts) != 0 {
				t.Errorf("dialog shown on a terminal: %q", scripts)
			}
		})
	}
}

func TestNotRunningDialog(t *testing.T) {
	got := notRunningDialog(`Alice said "hi" \ bye`)
	want := `display dialog "Alice said \"hi\" \\ bye" with title ` + appleScriptString(app.Name()) +
		` buttons {"OK"} default button "OK" with icon note giving up after 120` + "\n"
	if got != want {
		t.Errorf("notRunningDialog =\n%s\nwant\n%s", got, want)
	}
}

// TestWebUILauncher_MissingHomeNotCreated: a data directory that does not
// exist is not created, and the default address is used.
func TestWebUILauncher_MissingHomeNotCreated(t *testing.T) {
	clawHome := filepath.Join(t.TempDir(), "missing")
	t.Setenv("CLAW_HOME", clawHome)
	env := newLauncherEnv(nil, true, false)

	if err := env.launcher.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(clawHome); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("%s: %v; want it still missing", clawHome, err)
	}
	opened, _ := env.record()
	if want := config.DefaultConfig().Gateway.LocalHTTPURL(); len(opened) != 1 || opened[0] != want {
		t.Errorf("opened %q; want the default %q", opened, want)
	}
}

// TestWebUILauncher_SecretReferenceUnresolved: a secret reference whose
// variable is not set (as when the app is clicked) does not hide the
// configured port.
func TestWebUILauncher_SecretReferenceUnresolved(t *testing.T) {
	clawHome := t.TempDir()
	t.Setenv("CLAW_HOME", clawHome)
	t.Setenv("ALICE_UNSET_SECRET", "")
	if err := os.Unsetenv("ALICE_UNSET_SECRET"); err != nil {
		t.Fatal(err)
	}
	data := `{"gateway":{"port":18999},"tools":{"web":{"brave":{"api_key":"env:ALICE_UNSET_SECRET"}}}}`
	if err := os.WriteFile(filepath.Join(clawHome, "config.json"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	env := newLauncherEnv(nil, true, false)

	if err := env.launcher.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	opened, _ := env.record()
	if want := "http://127.0.0.1:18999/"; len(opened) != 1 || opened[0] != want {
		t.Errorf("opened %q; want once, %q", opened, want)
	}
}

// TestWebUILauncher_EmptyHomeGainsNothing: an existing, empty data
// directory is left empty.
func TestWebUILauncher_EmptyHomeGainsNothing(t *testing.T) {
	clawHome := t.TempDir()
	t.Setenv("CLAW_HOME", clawHome)
	env := newLauncherEnv(nil, true, false)

	if err := env.launcher.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(clawHome)
	if err != nil || len(entries) != 0 {
		t.Errorf("%s holds %v, %v; want nothing", clawHome, entries, err)
	}
}
