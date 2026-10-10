// ClawEh
// License: MIT

package config

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
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

// TestStore_UpdateRejectsNewDanglingModelReference: every save path goes
// through Update, so a save that adds a reference to a model that does not
// exist is refused there, naming the site and the alias.
func TestStore_UpdateRejectsNewDanglingModelReference(t *testing.T) {
	s := newTestStore(t)
	before := s.Current()
	err := s.Update(func(c *Config) error {
		c.Agents.List = append(c.Agents.List, AgentConfig{ID: "bob", Name: "Bob", Models: []string{"m", "ghost"}})
		return nil
	})
	verr, ok := errors.AsType[*ValidationError](err)
	if !ok {
		t.Fatalf("Update error = %v, want *ValidationError", err)
	}
	if want := `agents.list[bob].models: model "ghost" does not exist`; verr.Error() != want {
		t.Fatalf("error = %q, want %q", verr, want)
	}
	if s.Current() != before {
		t.Fatal("rejected config became current")
	}
	fromDisk, err := LoadConfig(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if len(fromDisk.Agents.List) != 1 {
		t.Fatal("rejected config was written to disk")
	}
}

// TestStore_UpdateKeepsPreExistingDanglingModelReference: a reference left
// dangling by an older release must not block an unrelated save (adding a
// provider), nor the save that fixes it.
func TestStore_UpdateKeepsPreExistingDanglingModelReference(t *testing.T) {
	s := newTestStore(t)
	// Simulate the old file: written directly, not through Update.
	cfg, err := LoadConfig(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	cfg.Agents.List[0].Models = []string{"DeepSeek 4 Pro", "m"}
	if err = SaveConfig(s.Path(), cfg); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Reload(); err != nil {
		t.Fatal(err)
	}

	err = s.Update(func(c *Config) error {
		c.Providers = append(c.Providers, Provider{Name: "other", Protocol: "openai-chat", BaseURL: "https://example.invalid/v1", APIKey: "k"})
		return nil
	})
	if err != nil {
		t.Fatalf("unrelated save refused: %v", err)
	}
	if got := s.Current(); len(got.Providers) != 2 || got.Agents.List[0].Models[0] != "DeepSeek 4 Pro" {
		t.Fatalf("save not applied as written: providers=%d models=%q", len(got.Providers), got.Agents.List[0].Models)
	}

	if err = s.Update(func(c *Config) error { c.Agents.List[0].Models = []string{"m"}; return nil }); err != nil {
		t.Fatalf("fixing save refused: %v", err)
	}
}

// TestStore_UpdateAcceptsPruneOnlyChange: the gateway removes missing-model
// references from the file through Update. A change that only removes
// references must pass the new-dangling-reference guard and be written, and
// with nothing left to remove the same callback is a no-op that leaves the
// file alone.
func TestStore_UpdateAcceptsPruneOnlyChange(t *testing.T) {
	s := newTestStore(t)
	cfg, err := LoadConfig(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	cfg.Agents.Defaults.Models = []string{"m"} // not the template's CLI aliases
	cfg.Agents.List[0].Models = []string{"DeepSeek 4 Pro", "m"}
	cfg.Agents.Defaults.ImageModel = "gone"
	if err = SaveConfig(s.Path(), cfg); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Reload(); err != nil {
		t.Fatal(err)
	}

	prune := func(removed *[]DanglingModelReference) func(c *Config) error {
		return func(c *Config) error {
			*removed = c.PruneDanglingModelReferences()
			if len(*removed) == 0 {
				return ErrUnchanged
			}
			return nil
		}
	}
	var removed []DanglingModelReference
	if err = s.Update(prune(&removed)); err != nil {
		t.Fatalf("prune-only Update refused: %v", err)
	}
	if len(removed) != 2 {
		t.Fatalf("removed = %+v, want 2 references", removed)
	}
	fromDisk, err := LoadConfig(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if got := fromDisk.Agents.List[0].Models; len(got) != 1 || got[0] != "m" {
		t.Fatalf("on-disk models = %q, want [m]", got)
	}
	if fromDisk.Agents.Defaults.ImageModel != "" {
		t.Fatalf("on-disk image_model = %q, want it cleared", fromDisk.Agents.Defaults.ImageModel)
	}

	info, err := os.Stat(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	before := s.Current()
	if err = s.Update(prune(&removed)); err != nil {
		t.Fatalf("second prune: %v", err)
	}
	if len(removed) != 0 || s.Current() != before {
		t.Fatalf("second prune changed something: removed=%+v", removed)
	}
	after, err := os.Stat(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(info.ModTime()) {
		t.Fatal("a prune with nothing to remove rewrote the file")
	}
}

// TestStore_EmptiedDefaultModelsStayEmpty: agents.defaults.models is filled
// from the template when the key is absent, so a save that empties it must
// write the empty list; otherwise the next load brings back models the
// operator (or the missing-model prune) removed.
func TestStore_EmptiedDefaultModelsStayEmpty(t *testing.T) {
	s := newTestStore(t)
	if err := s.Update(func(c *Config) error { c.Agents.Defaults.Models = []string{}; return nil }); err != nil {
		t.Fatalf("Update: %v", err)
	}
	fromDisk, err := LoadConfig(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if got := fromDisk.Agents.Defaults.Models; len(got) != 0 {
		t.Fatalf("agents.defaults.models after reload = %q, want empty", got)
	}
}

// Update reports every refusal together: those its callback raised as a
// ValidationError, the model, human and mount rules, the listener settings
// (all of them, not the first), the forum limits and the agent ids.
func TestStoreUpdate_ReportsEveryRefusal(t *testing.T) {
	s := newTestStore(t)
	err := s.Update(func(c *Config) error {
		c.Agents.List[0].Models = []string{"Ghost"}
		c.Agents.List = append(c.Agents.List, AgentConfig{ID: "Bob"})
		c.Gateway.Port = 70000
		c.MCPHost.Listen = "0.0.0.0:5911"
		c.Forum.Limits.MaxCalls = -1
		return &ValidationError{Err: errors.New("The caller refused this.")}
	})
	verr, ok := errors.AsType[*ValidationError](err)
	if !ok {
		t.Fatalf("Update = %v, want a ValidationError", err)
	}
	want := []string{
		"The caller refused this.",
		`agents.list[main].models: model "Ghost" does not exist`,
		"gateway.port 70000 is out of valid range (1-65535)",
		`mcp_host.listen "0.0.0.0:5911": the MCP host is plain HTTP and must listen on a loopback address (127.0.0.1 or ::1)`,
		"The forum maximum for max_calls must be 0 (the default) or more.",
		`Agent id "Bob" may use only lower-case letters, digits, - and _; use "bob".`,
	}
	if got := verr.Messages(); !slices.Equal(got, want) {
		t.Fatalf("Messages() =\n%q\nwant\n%q", got, want)
	}
	if s.Current().Gateway.Port == 70000 {
		t.Error("a refused update became current")
	}
}

// validateListeners names every listener setting at fault, not the first.
func TestValidateListeners_JoinsEveryFailure(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Gateway.Port = -1
	cfg.Gateway.TLSPort = 70000
	cfg.MCPHost.Listen = "0.0.0.0:5911"
	got := errorMessages(cfg.validateListeners())
	want := []string{
		"gateway.port -1 is out of valid range (1-65535)",
		"gateway.tls_port 70000 is out of valid range (1-65535)",
		`mcp_host.listen "0.0.0.0:5911": the MCP host is plain HTTP and must listen on a loopback address (127.0.0.1 or ::1)`,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("validateListeners =\n%q\nwant\n%q", got, want)
	}
}
