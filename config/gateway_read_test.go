// ClawEh
// License: MIT

package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadGateway(t *testing.T) {
	dir := t.TempDir()
	defaults := DefaultConfig().Gateway

	t.Run("port with an unresolvable secret elsewhere", func(t *testing.T) {
		t.Setenv("ALICE_UNSET_SECRET", "")
		if err := os.Unsetenv("ALICE_UNSET_SECRET"); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "secret.json")
		data := `{"gateway":{"port":18999},"tools":{"web":{"brave":{"api_key":"env:ALICE_UNSET_SECRET"}}}}`
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadConfig(path); err == nil {
			t.Fatal("LoadConfig resolved a secret whose variable is not set; the case tests nothing")
		}
		gw, err := ReadGateway(path)
		if err != nil {
			t.Fatal(err)
		}
		if gw.Port != 18999 {
			t.Errorf("port %d; want 18999", gw.Port)
		}
		if gw.Host != defaults.Host || gw.EffectiveTLSPort() != defaults.EffectiveTLSPort() {
			t.Errorf("gateway %+v; want the other fields at their defaults", gw)
		}
	})
	t.Run("missing file", func(t *testing.T) {
		gw, err := ReadGateway(filepath.Join(dir, "missing.json"))
		if err != nil {
			t.Fatal(err)
		}
		if gw.Port != defaults.Port || gw.Host != defaults.Host {
			t.Errorf("gateway %+v; want the defaults", gw)
		}
	})
	t.Run("invalid JSON", func(t *testing.T) {
		path := filepath.Join(dir, "bad.json")
		if err := os.WriteFile(path, []byte(`{"gateway":`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadGateway(path); err == nil {
			t.Error("invalid JSON read without an error")
		}
	})
}
