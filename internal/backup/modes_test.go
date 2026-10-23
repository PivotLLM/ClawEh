//go:build !windows

package backup

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/config"
)

// An existing destination is only ever tightened: a 0700 one stays 0700 under
// a 0750 setting, and a 0777 one loses what 0750 does not grant.
func TestRunDestTightenOnly(t *testing.T) {
	home := t.TempDir()
	writeFileT(t, filepath.Join(home, "config.json"), `{}`, 0o600)
	src := Source{
		Home: home, ConfigPath: filepath.Join(home, "config.json"),
		CronDir: filepath.Join(home, "cron"), AgentsDir: filepath.Join(home, "agents"),
		Modes: config.Modes{Dir: 0o750, File: 0o640},
	}
	for from, want := range map[os.FileMode]os.FileMode{0o700: 0o700, 0o777: 0o750} {
		dest := t.TempDir()
		if err := os.Chmod(dest, from); err != nil {
			t.Fatal(err)
		}
		if _, err := Run(src, dest, time.Now(), nil); err != nil {
			t.Fatal(err)
		}
		fi, err := os.Stat(dest)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("dest from %04o: %04o, want %04o", from, got, want)
		}
	}
}

// With security.permissions.backup set, the destination directory and the
// archive take its modes.
func TestRunConfiguredModes(t *testing.T) {
	old := syscall.Umask(0o022)
	defer syscall.Umask(old)
	home := t.TempDir()
	writeFileT(t, filepath.Join(home, "config.json"), `{}`, 0o600)
	src := Source{
		Home: home, ConfigPath: filepath.Join(home, "config.json"),
		CronDir: filepath.Join(home, "cron"), AgentsDir: filepath.Join(home, "agents"),
		Modes: config.Modes{Dir: 0o750, File: 0o640},
	}
	dest := filepath.Join(t.TempDir(), "out")
	res, err := Run(src, dest, time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{dest: 0o750, res.Archive: 0o640} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("%s mode = %04o, want %04o", path, got, want)
		}
	}
}
