package gateway

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/internal/testalerts"
)

// openStore writes body to config.json in dir and opens the store on it.
func openStore(t *testing.T, dir, body string) *config.Store {
	t.Helper()
	t.Setenv("CLAW_HOME", dir)
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := config.NewStore(path)
	if err != nil {
		t.Fatalf("config.NewStore: %v", err)
	}
	return store
}

// TestRuntimeConfig_RemovesMissingModelReferenceFromFile is the boot path:
// the missing-model reference is removed from config.json and the runtime
// copy, reported as removed, and a second pass writes nothing.
func TestRuntimeConfig_RemovesMissingModelReferenceFromFile(t *testing.T) {
	rec := testalerts.Install(t)
	store := openStore(t, t.TempDir(), danglingRefConfigJSON("boot"))

	cfg, prune, err := runtimeConfig(store)
	if err != nil {
		t.Fatalf("runtimeConfig: %v", err)
	}
	if got := cfg.Agents.List[0].Models; !slices.Equal(got, []string{"good"}) {
		t.Fatalf("runtime models = %q, want [good]", got)
	}
	want := []config.DanglingModelReference{{Site: "agents.list[Alice].models", Alias: "DeepSeek 4 Pro", Agent: "Alice"}}
	if !slices.Equal(prune.removed, want) || len(prune.skipped) != 0 {
		t.Fatalf("prune = %+v, want removed %+v and nothing skipped", prune, want)
	}
	onDisk, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(onDisk), "DeepSeek 4 Pro") {
		t.Fatalf("config file still names the missing model:\n%s", onDisk)
	}
	(&modelRefAlerts{}).report(rec, prune)
	if got := rec.Alerts(); len(got) != 1 || got[0].Description != aliceRemovedDesc {
		t.Fatalf("alerts = %+v, want one with the removed description", got)
	}

	info, err := os.Stat(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if _, prune, err = runtimeConfig(store); err != nil {
		t.Fatal(err)
	}
	if prune.persisted() || len(prune.skipped) != 0 {
		t.Fatalf("second pass = %+v, want nothing to do", prune)
	}
	after, err := os.Stat(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(info.ModTime()) {
		t.Fatal("a pass with nothing to remove rewrote the file")
	}
}

// TestRuntimeConfig_InvalidModelStaysInFile: a model dropped by PruneInvalid
// (its provider is missing) is still defined in the file, so a reference to it
// is skipped in the runtime copy only; neither the model nor the reference is
// removed from config.json, where the operator can repair the provider.
func TestRuntimeConfig_InvalidModelStaysInFile(t *testing.T) {
	body := `{
		"providers": [{"name": "p", "protocol": "openai-chat", "base_url": "https://example.invalid/v1", "api_key": "k"}],
		"models": [
			{"model_name": "good", "model": "gpt-4o", "provider": "p", "enabled": true},
			{"model_name": "orphan", "model": "gpt-4o", "provider": "no-such-provider", "enabled": true}
		],
		"agents": {
			"defaults": {"models": []},
			"list": [{"id": "Alice", "name": "Alice", "default": true, "models": ["orphan", "good"]}]
		}
	}`
	store := openStore(t, t.TempDir(), body)

	cfg, prune, err := runtimeConfig(store)
	if err != nil {
		t.Fatalf("runtimeConfig: %v", err)
	}
	if got := cfg.Agents.List[0].Models; !slices.Equal(got, []string{"good"}) {
		t.Fatalf("runtime models = %q, want [good]", got)
	}
	if prune.persisted() || len(prune.skipped) != 1 || prune.skipped[0].Alias != "orphan" {
		t.Fatalf("prune = %+v, want only orphan skipped", prune)
	}
	onDisk, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk) != body {
		t.Fatal("config file was rewritten for an invalid (not missing) model")
	}
}

// TestRuntimeConfig_ReadOnlyConfigFallsBackToRuntimePrune: when config.json
// cannot be rewritten (its directory is read-only, so the atomic replace
// fails), the reference is still dropped from the runtime copy and reported
// as skipped for the alert, and no error is returned: startup must not abort
// over this.
func TestRuntimeConfig_ReadOnlyConfigFallsBackToRuntimePrune(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions, so the write cannot be made to fail")
	}
	rec := testalerts.Install(t)
	dir := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := danglingRefConfigJSON("read-only")
	store := openStore(t, dir, body)
	if err := os.Chmod(store.Path(), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Restore write access so t.TempDir can remove the directory.
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Errorf("restore directory permissions: %v", err)
		}
	})

	cfg, prune, err := runtimeConfig(store)
	if err != nil {
		t.Fatalf("runtimeConfig with a read-only config: %v", err)
	}
	if got := cfg.Agents.List[0].Models; !slices.Equal(got, []string{"good"}) {
		t.Fatalf("runtime models = %q, want [good]", got)
	}
	if prune.persisted() || len(prune.skipped) != 1 || prune.skipped[0].Alias != "DeepSeek 4 Pro" {
		t.Fatalf("prune = %+v, want the reference skipped, not removed", prune)
	}
	if got := store.Current().Agents.List[0].Models; !slices.Equal(got, []string{"DeepSeek 4 Pro", "good"}) {
		t.Fatalf("store models = %q, want unchanged after the failed write", got)
	}
	onDisk, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk) != body {
		t.Fatal("read-only config file changed")
	}
	(&modelRefAlerts{}).report(rec, prune)
	if got := rec.Alerts(); len(got) != 1 || got[0].EventID != "model-ref:agents.list[Alice].models" {
		t.Fatalf("alerts = %+v, want one missing-model alert", got)
	}
}
