//go:build !windows

// ClawEh
// License: MIT

package devicegw

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/internal/perms"
	"github.com/PivotLLM/ClawEh/utils"
)

// TestOpenStoreKeepsSecretsMode: `claw devicegw` opens the pairing database
// with security.permissions.secrets from the config, not the built-in 0600,
// so it does not undo the mode the gateway set.
func TestOpenStoreKeepsSecretsMode(t *testing.T) {
	t.Cleanup(func() { perms.SetSecretModes(config.Modes{Dir: 0o700, File: 0o600}) })
	home := t.TempDir()
	t.Setenv(global.EnvVarHome, home)
	body := `{"security":{"permissions":{"secrets":{"dir_mode":"0750","file_mode":"0640"}}}}`
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(home, global.InternalDir, "gateway.db")
	if err := os.MkdirAll(filepath.Dir(db), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(db, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	store, _, err := openStore()
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	utils.CloseQuietly(store)
	fi, err := os.Stat(db)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o640 {
		t.Errorf("gateway.db = %04o, want the configured 0640", got)
	}
}
