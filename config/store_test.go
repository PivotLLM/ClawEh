// ClawEh
// License: MIT

package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CLAW_HOME", dir)
	p := filepath.Join(dir, "config.json")
	cfg := DefaultConfig()
	cfg.Providers = []Provider{{Name: "openai", Protocol: "openai-chat", BaseURL: "https://api.openai.com/v1", APIKey: "sk-default-0123456789"}}
	cfg.Models = []ModelConfig{{ModelName: "m", Model: "gpt-4o", Provider: "openai", Enabled: true}}
	cfg.Agents.List = []AgentConfig{{ID: "main", Name: "Main", Default: true}}
	if err := SaveConfig(p, cfg); err != nil {
		t.Fatal(err)
	}
	s, err := NewStore(p)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

func TestStore_CurrentUpdateReload(t *testing.T) {
	s := newTestStore(t)
	before := s.Current()
	if before.Providers[0].Name != "openai" {
		t.Fatalf("Current() did not load the file")
	}
	if before.DataDir() == "" {
		t.Fatal("loaded config has no data dir")
	}

	err := s.Update(func(c *Config) error {
		c.Providers[0].Name = "renamed"
		return nil
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if before.Providers[0].Name != "openai" {
		t.Fatal("Update mutated the snapshot a reader already holds")
	}
	after := s.Current()
	if after.Providers[0].Name != "renamed" {
		t.Fatal("Update did not swap the current config")
	}
	if after.DataDir() != before.DataDir() {
		t.Fatal("Update lost the runtime data dir")
	}
	if after.DefaultConfig {
		t.Fatal("a save must clear default_config")
	}

	// The file has it too.
	fromDisk, err := LoadConfig(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if fromDisk.Providers[0].Name != "renamed" {
		t.Fatal("Update did not persist")
	}

	// Reload picks up an external edit.
	fromDisk.Providers[0].Name = "edited-outside"
	if err = SaveConfig(s.Path(), fromDisk); err != nil {
		t.Fatal(err)
	}
	if s.Current().Providers[0].Name != "renamed" {
		t.Fatal("store must not see a file change before Reload")
	}
	reloaded, err := s.Reload()
	if err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if reloaded.Providers[0].Name != "edited-outside" || s.Current() != reloaded {
		t.Fatal("Reload did not make the file's config current")
	}
}

func TestStore_UpdateErrorLeavesEverythingUnchanged(t *testing.T) {
	s := newTestStore(t)
	before := s.Current()
	boom := errors.New("boom")
	err := s.Update(func(c *Config) error {
		c.Providers[0].Name = "changed"
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("Update error = %v, want the callback's", err)
	}
	if s.Current() != before {
		t.Fatal("a failed Update swapped the config")
	}
	fromDisk, err := LoadConfig(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if fromDisk.Providers[0].Name != "openai" {
		t.Fatal("a failed Update wrote to disk")
	}
}

func TestStore_UpdateRejectsWhatLoadConfigRefuses(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"tls one of two", func(c *Config) { c.Gateway.TLS.CertFile = "/etc/claw/cert.pem" }, "gateway.tls.cert_file and gateway.tls.key_file"},
		{"mcp host off-box", func(c *Config) { c.MCPHost.Listen = "0.0.0.0:5911" }, "loopback"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			before := s.Current()
			err := s.Update(func(c *Config) error { tc.mutate(c); return nil })
			verr, ok := errors.AsType[*ValidationError](err)
			if !ok {
				t.Fatalf("Update error = %v, want *ValidationError", err)
			}
			if !strings.Contains(verr.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", verr, tc.want)
			}
			if s.Current() != before {
				t.Fatal("invalid config became current")
			}
			fromDisk, err := LoadConfig(s.Path())
			if err != nil {
				t.Fatal(err)
			}
			if fromDisk.Gateway.TLS.CertFile != "" || fromDisk.MCPHost.Listen == "0.0.0.0:5911" {
				t.Fatal("invalid config was written to disk")
			}
		})
	}
}

func TestStore_ParallelUpdatesNeverLoseAWrite(t *testing.T) {
	s := newTestStore(t)
	start := s.Current().ConfigReloadIntervalSeconds
	const n = 32
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for range n {
		wg.Go(func() {
			errs <- s.Update(func(c *Config) error {
				c.ConfigReloadIntervalSeconds++
				return nil
			})
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("Update: %v", err)
		}
	}
	if got := s.Current().ConfigReloadIntervalSeconds; got != start+n {
		t.Fatalf("counter = %d, want %d: a parallel Update lost a write", got, start+n)
	}
	fromDisk, err := LoadConfig(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if fromDisk.ConfigReloadIntervalSeconds != start+n {
		t.Fatalf("on disk = %d, want %d", fromDisk.ConfigReloadIntervalSeconds, start+n)
	}
}

func TestStore_UpdateResolvesAndPreservesSecretReference(t *testing.T) {
	t.Setenv("CLAW_TEST_STORE_KEY", "sk-live-value-0123456789")
	s := newTestStore(t)

	// The WebUI submits the reference as a literal string in the field.
	err := s.Update(func(c *Config) error {
		c.Providers[0].APIKey = "env:CLAW_TEST_STORE_KEY"
		return nil
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got := s.Current().Providers[0].APIKey; got != "sk-live-value-0123456789" {
		t.Fatalf("in-memory api_key = %q, want the resolved value", got)
	}
	if got := providerAPIKeyOnDisk(t, s.Path(), 0); got != "env:CLAW_TEST_STORE_KEY" {
		t.Fatalf("on disk = %q, want the reference", got)
	}

	// An unrelated Update keeps the reference on disk.
	if err = s.Update(func(c *Config) error { c.Providers[0].BaseURL = "https://example.test/v1"; return nil }); err != nil {
		t.Fatal(err)
	}
	if got := providerAPIKeyOnDisk(t, s.Path(), 0); got != "env:CLAW_TEST_STORE_KEY" {
		t.Fatalf("after unrelated update on disk = %q, want the reference kept", got)
	}

	// A missing variable is a validation error and changes nothing.
	if err = os.Unsetenv("CLAW_TEST_STORE_MISSING"); err != nil {
		t.Fatal(err)
	}
	err = s.Update(func(c *Config) error { c.Providers[0].APIKey = "env:CLAW_TEST_STORE_MISSING"; return nil })
	var verr *ValidationError
	if !errors.As(err, &verr) || !strings.Contains(err.Error(), "providers[0].api_key") {
		t.Fatalf("Update error = %v, want a ValidationError naming providers[0].api_key", err)
	}
	if got := s.Current().Providers[0].APIKey; got != "sk-live-value-0123456789" {
		t.Fatalf("failed Update changed the live key to %q", got)
	}
}

func TestNewStore_MissingFileLoadsDefaults(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAW_HOME", dir)
	s, err := NewStore(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if !s.Current().DefaultConfig {
		t.Fatal("a missing file must load as the defaults")
	}
}
