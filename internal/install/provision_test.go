package install

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/PivotLLM/ClawEh/internal/admin"
)

// stubInstallSeams replaces the service registration with a recorder and the
// account prompt's terminal with open, restoring both after the test.
func stubInstallSeams(t *testing.T, open func(io.Writer) (*admin.Terminal, error)) *int {
	t.Helper()
	origRegister, origTerminal := registerService, adminTerminal
	t.Cleanup(func() { registerService, adminTerminal = origRegister, origTerminal })
	calls := 0
	registerService = func(*TargetUser, string, string, string) error {
		calls++
		return nil
	}
	adminTerminal = open
	return &calls
}

func noTerminal(io.Writer) (*admin.Terminal, error) {
	return nil, fmt.Errorf("%w: /dev/tty: no such device or address", admin.ErrNoTerminal)
}

func TestProvisionAndRegister_NoAccountNoTerminalStopsBeforeService(t *testing.T) {
	t.Setenv(admin.EnvUser, "")
	t.Setenv(admin.EnvPassword, "")
	calls := stubInstallSeams(t, noTerminal)
	clawHome := filepath.Join(t.TempDir(), "claw")
	tu := &TargetUser{Username: "tester"}

	_, err := provisionAndRegister(tu, "/nonexistent/claw", "/nonexistent", clawHome, false)
	if !errors.Is(err, admin.ErrNoAccountNoTerminal) {
		t.Fatalf("provisionAndRegister = %v; want ErrNoAccountNoTerminal", err)
	}
	for _, want := range []string{"export CLAW_ADMIN_USER and CLAW_ADMIN_PASSWORD", "`claw admin`", "was not registered or started"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q:\n%v", want, err)
		}
	}
	if *calls != 0 {
		t.Fatalf("service registered %d time(s) without an admin account", *calls)
	}
	if _, serr := os.Stat(admin.Path(clawHome)); !errors.Is(serr, os.ErrNotExist) {
		t.Fatalf("credentials file exists: %v", serr)
	}
}

func TestProvisionAndRegister_EnvAccountThenService(t *testing.T) {
	t.Setenv(admin.EnvUser, "jane")
	t.Setenv(admin.EnvPassword, "jane's long password")
	calls := stubInstallSeams(t, func(io.Writer) (*admin.Terminal, error) {
		t.Error("terminal opened although the environment supplies the account")
		return noTerminal(nil)
	})
	clawHome := filepath.Join(t.TempDir(), "claw")

	user, err := provisionAndRegister(&TargetUser{Username: "tester"}, "/nonexistent/claw", "/nonexistent", clawHome, false)
	if err != nil || user != "jane" {
		t.Fatalf("provisionAndRegister = %q, %v; want jane, nil", user, err)
	}
	if *calls != 1 {
		t.Fatalf("service registered %d time(s), want 1", *calls)
	}
	fi, err := os.Stat(clawHome)
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("CLAW_HOME %v, err %v; want a 0700 directory", fi, err)
	}
	creds, err := admin.Load(admin.Path(clawHome))
	if err != nil || !creds.Verify("jane", "jane's long password") {
		t.Fatalf("Load/Verify: %v", err)
	}
}

func TestProvisionAndRegister_ExistingAccountKept(t *testing.T) {
	t.Setenv(admin.EnvUser, "")
	t.Setenv(admin.EnvPassword, "")
	calls := stubInstallSeams(t, noTerminal) // must not be needed
	clawHome := t.TempDir()
	if err := admin.Write(admin.Path(clawHome), "kim", "kim's long password"); err != nil {
		t.Fatal(err)
	}

	user, err := provisionAndRegister(&TargetUser{Username: "tester"}, "/nonexistent/claw", "/nonexistent", clawHome, true)
	if err != nil || user != "kim" || *calls != 1 {
		t.Fatalf("provisionAndRegister = %q, %v, registrations %d; want kim, nil, 1", user, err, *calls)
	}
}

func TestProvisionAndRegister_RootChownsToServiceAccount(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("handing files to another account needs root")
	}
	t.Setenv(admin.EnvUser, "lee")
	t.Setenv(admin.EnvPassword, "lee's long password")
	stubInstallSeams(t, noTerminal)
	clawHome := filepath.Join(t.TempDir(), "claw")
	tu := &TargetUser{Username: "nobody", UID: "65534", GID: "65534", IsRoot: true}

	if _, err := provisionAndRegister(tu, "/nonexistent/claw", "/nonexistent", clawHome, false); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{clawHome, admin.Path(clawHome)} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok || st.Uid != 65534 || st.Gid != 65534 {
			t.Errorf("%s owner = %+v; want 65534:65534", p, fi.Sys())
		}
	}
}
