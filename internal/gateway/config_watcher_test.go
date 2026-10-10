package gateway

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tenebris-tech/alerter"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/internal/testalerts"
)

// validConfigJSON is the minimal config the watcher's LoadConfig+ValidateModels
// accepts. An empty models is valid (validation only rejects malformed lists),
// but agents.defaults.models must then be empty too: left out, it would inherit
// the template's CLI aliases, which reference models this file does not have.
const validConfigJSON = `{"models":[],"agents":{"defaults":{"models":[]}}}`

// seedStore writes validConfigJSON to a fresh config.json and opens the store
// the watcher polls; it returns the store and the file's path.
func seedStore(t *testing.T) (*config.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(validConfigJSON), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	store, err := config.NewStore(path)
	if err != nil {
		t.Fatalf("config.NewStore: %v", err)
	}
	return store, path
}

func writeConfig(t *testing.T, path, extra string) {
	t.Helper()
	body := `{"models":[],"agents":{"defaults":{"models":[]}},"_marker":"` + extra + `"}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

// TestConfigWatcher_DebouncesBurstIntoSingleReload verifies that a burst of
// writes within the debounce window collapses into exactly one reload, and that
// each write resets the quiet timer (no reload until the file goes quiet).
func TestConfigWatcher_DebouncesBurstIntoSingleReload(t *testing.T) {
	store, path := seedStore(t)

	// The burst outlasts the debounce window, so a reload during it would mean
	// a write did not reset the timer; each gap is a tenth of the window, so a
	// scheduling delay would have to be ten times the gap to fire one early.
	interval := 10 * time.Millisecond
	debounce := 400 * time.Millisecond
	gap := debounce / 10
	ch, stop, _ := setupConfigWatcherPolling(store, interval, debounce, false, alerter.Nop{}, &modelRefAlerts{})
	defer stop()

	start := time.Now()
	for i := 0; time.Since(start) < debounce+5*gap; i++ {
		// A different length every time, so the change is seen even where the
		// file system's modification times are coarse.
		writeConfig(t, path, strings.Repeat("x", i+1))
		time.Sleep(gap)
	}

	// Nothing should have been delivered yet (the timer kept resetting).
	select {
	case <-ch:
		t.Fatal("reload fired during the burst; debounce did not reset the timer")
	default:
	}

	// After quiescence (> debounce), exactly one reload should arrive.
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("expected one reload after the file went quiet; got none")
	}

	// And no second reload for the same settled state.
	select {
	case <-ch:
		t.Fatal("unexpected second reload for an unchanged file")
	case <-time.After(debounce + 100*time.Millisecond):
	}
}

// TestConfigWatcher_MarkAppliedSuppressesReload verifies that after a change is
// written and markApplied() is called (as the force-reload path does), the
// watcher advances its baseline and does NOT fire a redundant reload — the
// double-reload that was tearing down an active chat after the setup wizard.
func TestConfigWatcher_MarkAppliedSuppressesReload(t *testing.T) {
	store, path := seedStore(t)

	interval := 10 * time.Millisecond
	debounce := 80 * time.Millisecond
	ch, stop, markApplied := setupConfigWatcherPolling(store, interval, debounce, false, alerter.Nop{}, &modelRefAlerts{})
	defer stop()

	// Simulate a force-reload: the config changes and the out-of-band path
	// applies it, then tells the watcher via markApplied().
	writeConfig(t, path, "force-applied")
	markApplied(configFileStateOf(path))

	// The watcher must not deliver a reload for the already-applied change.
	select {
	case <-ch:
		t.Fatal("watcher fired a redundant reload after markApplied()")
	case <-time.After(debounce + 200*time.Millisecond):
	}

	// A genuinely new change after markApplied still triggers a reload.
	writeConfig(t, path, "later-edit")
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("a new change after markApplied should still reload")
	}
}

// TestConfigWatcher_MarkAppliedDropsQueuedReload: a forced reload can take
// longer than the watcher's debounce, so the watcher may already have queued
// a reload for the same change by the time markApplied is called. That reload
// is dropped; it would apply the change a second time.
func TestConfigWatcher_MarkAppliedDropsQueuedReload(t *testing.T) {
	store, path := seedStore(t)

	interval := 10 * time.Millisecond
	debounce := 50 * time.Millisecond
	ch, stop, markApplied := setupConfigWatcherPolling(store, interval, debounce, false, alerter.Nop{}, &modelRefAlerts{})
	defer stop()
	// markApplied returns once the watcher has taken the seed file as its
	// baseline, so the write below is a change it sees.
	markApplied(configFileStateOf(path))

	writeConfig(t, path, "force-applied")
	applied := configFileStateOf(path) // what the forced reload reads
	waitUntil(t, "the watcher to queue a reload", func() bool { return len(ch) == 1 })
	markApplied(applied)
	if len(ch) != 0 {
		t.Fatal("the reload queued for the applied change is still queued after markApplied")
	}
	select {
	case <-ch:
		t.Fatal("watcher fired a redundant reload after markApplied()")
	case <-time.After(debounce + 200*time.Millisecond):
	}

	writeConfig(t, path, "later-edit")
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("a new change after markApplied should still reload")
	}
}

// TestConfigWatcher_MarkAppliedKeepsLaterChange: a change written after the
// forced reload read the file is newer than the state it marks applied, so it
// is still reloaded even though markApplied dropped the queued reload.
func TestConfigWatcher_MarkAppliedKeepsLaterChange(t *testing.T) {
	store, path := seedStore(t)

	interval := 10 * time.Millisecond
	debounce := 50 * time.Millisecond
	ch, stop, markApplied := setupConfigWatcherPolling(store, interval, debounce, false, alerter.Nop{}, &modelRefAlerts{})
	defer stop()
	markApplied(configFileStateOf(path))

	writeConfig(t, path, "force-applied")
	applied := configFileStateOf(path)
	writeConfig(t, path, "written while the forced reload ran, longer")
	waitUntil(t, "the watcher to queue a reload", func() bool { return len(ch) == 1 })
	markApplied(applied)

	select {
	case got := <-ch:
		if got == nil {
			t.Fatal("nil config reloaded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the change written after the forced reload read the file was not reloaded")
	}
}

// waitUntil polls cond until it holds, failing the test after ten seconds.
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestConfigWatcher_RetriesWhenConsumerBusy verifies that a change which cannot
// be delivered because the consumer is still draining a previous reload is NOT
// lost: once the consumer catches up, the pending change is delivered. This
// guards the bug where the applied marker advanced on a dropped send, so
// enabling an agent's tool suite silently required a restart.
func TestConfigWatcher_RetriesWhenConsumerBusy(t *testing.T) {
	store, path := seedStore(t)

	interval := 10 * time.Millisecond
	debounce := 60 * time.Millisecond
	ch, stop, markApplied := setupConfigWatcherPolling(store, interval, debounce, false, alerter.Nop{}, &modelRefAlerts{})
	defer stop()

	// The watcher takes the seed file as its baseline before the first real
	// change, so A is reliably detected as new (markApplied returns once it has).
	markApplied(configFileStateOf(path))

	// First change A is delivered into the cap-1 buffer; we intentionally do NOT
	// read it yet, simulating a consumer still busy with a prior reload. Poll until
	// A is actually buffered so the next write is guaranteed to find a full buffer.
	writeConfig(t, path, "A")
	deadline := time.Now().Add(3 * time.Second)
	for len(ch) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("first reload (A) was never delivered to the buffer")
		}
		time.Sleep(interval)
	}

	// Second change B lands while the buffer is full → the send is dropped. With
	// the fix the watcher keeps retrying instead of advancing the applied marker.
	writeConfig(t, path, "BB")
	time.Sleep(5 * debounce) // let the debounce elapse and the dropped send retry

	// Drain A (consumer catches up).
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("expected the first reload (A) to be buffered")
	}

	// B must still be delivered — not lost.
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("change B was lost: watcher advanced past it without delivering")
	}
}

// danglingRefConfigJSON has an agent whose first model was deleted before the
// delete guard existed: "DeepSeek 4 Pro" is referenced but not defined. marker
// varies the file so an unrelated edit can be written.
func danglingRefConfigJSON(marker string) string {
	return `{
		"providers": [{"name": "p", "protocol": "openai-chat", "base_url": "https://example.invalid/v1", "api_key": "k"}],
		"models": [{"model_name": "good", "model": "gpt-4o", "provider": "p", "enabled": true}],
		"agents": {
			"defaults": {"models": []},
			"list": [{"id": "alice", "name": "Alice", "default": true, "models": ["DeepSeek 4 Pro", "good"]}]
		},
		"_marker": "` + marker + `"
	}`
}

// waitReload returns the next reloaded config, failing the test if none comes.
func waitReload(t *testing.T, ch <-chan *config.Config) *config.Config {
	t.Helper()
	select {
	case cfg := <-ch:
		return cfg
	case <-time.After(3 * time.Second):
		t.Fatal("expected a reload; got none")
		return nil
	}
}

// aliceRemovedDesc is the alert description for Alice's removed reference.
const aliceRemovedDesc = `Alice listed model "DeepSeek 4 Pro", which no longer exists; it was removed from Alice's model list and the next model in the list is now used. Nothing else to do — check Alice's models on the Agents page if you want a different one.`

// TestConfigWatcher_DanglingModelReferenceIsRemovedFromFile: an old reference
// to a deleted model must not block an unrelated change. The reload is applied
// with the reference removed from config.json as well as the runtime copy (the
// agent falls through to its next model) and one alert is raised for it. The
// watcher's own rewrite is not reloaded again: no second reload, no second
// alert, no second write.
func TestConfigWatcher_DanglingModelReferenceIsRemovedFromFile(t *testing.T) {
	rec := testalerts.Install(t)
	store, path := seedStore(t)

	interval := 10 * time.Millisecond
	debounce := 50 * time.Millisecond
	ch, stop, markApplied := setupConfigWatcherPolling(store, interval, debounce, false, rec, &modelRefAlerts{})
	defer stop()
	markApplied(configFileStateOf(path)) // the seed file is the watcher's baseline

	if err := os.WriteFile(path, []byte(danglingRefConfigJSON("first")), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := waitReload(t, ch)

	if got := cfg.Agents.List[0].Models; !slices.Equal(got, []string{"good"}) {
		t.Fatalf("runtime agent models = %q, want [good] (dangling entry removed, next model first)", got)
	}
	if got := store.Current().Agents.List[0].Models; !slices.Equal(got, []string{"good"}) {
		t.Fatalf("store models = %q, want [good]", got)
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(onDisk), "DeepSeek 4 Pro") {
		t.Fatalf("config file still names the missing model:\n%s", onDisk)
	}
	fixed, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	const wantEvent = "model-ref:agents.list[alice].models"
	got := rec.Alerts()
	if len(got) != 1 {
		t.Fatalf("alerts = %+v, want exactly one", got)
	}
	if got[0].EventID != wantEvent || got[0].Title != "Agent references a missing model" {
		t.Fatalf("alert = %+v, want EventID %q and the missing-model title", got[0], wantEvent)
	}
	if got[0].Description != aliceRemovedDesc {
		t.Fatalf("description = %q, want %q", got[0].Description, aliceRemovedDesc)
	}
	if got[0].Priority != alerter.Normal {
		t.Fatalf("alert priority = %d, want Normal", got[0].Priority)
	}

	// The watcher's own rewrite is not a change to apply.
	select {
	case <-ch:
		t.Fatal("the watcher reloaded its own rewrite of the file")
	case <-time.After(debounce + 300*time.Millisecond):
	}
	if n := len(rec.Alerts()); n != 1 {
		t.Fatalf("alerts after the rewrite settled = %d, want still 1", n)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(fixed.ModTime()) {
		t.Fatal("the config file was written again after the fix")
	}

	// An unrelated edit with nothing to remove reloads without a write or an
	// alert.
	if err = os.WriteFile(path, []byte(strings.Replace(string(onDisk), "first", "second-edit", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	edited, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg = waitReload(t, ch)
	if !slices.Equal(cfg.Agents.List[0].Models, []string{"good"}) {
		t.Fatalf("second reload models = %q, want [good]", cfg.Agents.List[0].Models)
	}
	if n := len(rec.Alerts()); n != 1 {
		t.Fatalf("alerts after an unrelated edit = %d, want still 1", n)
	}
	now, err := os.ReadFile(path)
	if err != nil || string(now) != string(edited) {
		t.Fatalf("an edit with nothing to remove was rewritten (err=%v)", err)
	}

	// The reference put back is removed and alerted again.
	if err = os.WriteFile(path, []byte(danglingRefConfigJSON("third")), 0o600); err != nil {
		t.Fatal(err)
	}
	waitReload(t, ch)
	if n := len(rec.Alerts()); n != 2 {
		t.Fatalf("alerts after the reference came back = %d, want 2", n)
	}
}

// TestConfigWatcher_InvalidBindingStillRejected: a file that is genuinely
// invalid is still refused, with the previous config kept and a
// "Config file invalid" alert.
func TestConfigWatcher_InvalidBindingStillRejected(t *testing.T) {
	rec := testalerts.Install(t)
	store, path := seedStore(t)

	interval := 10 * time.Millisecond
	debounce := 50 * time.Millisecond
	ch, stop, markApplied := setupConfigWatcherPolling(store, interval, debounce, false, rec, &modelRefAlerts{})
	defer stop()
	markApplied(configFileStateOf(path)) // the seed file is the watcher's baseline

	body := `{"models":[],"agents":{"defaults":{"models":[]}},
		"bindings":[{"agent_id":"main","default":true,"match":{"channel":""}}]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ch:
		t.Fatal("a config with an invalid default binding was applied")
	case <-time.After(debounce + 300*time.Millisecond):
	}
	got := rec.Alerts()
	if len(got) != 1 || got[0].EventID != "config" || got[0].Title != "Config file invalid" {
		t.Fatalf("alerts = %+v, want one Config file invalid alert", got)
	}
}

// TestModelRefAlerts_SkippedOncePerReferenceUntilFixed: a reference skipped
// at runtime (still in the file) alerts the first time it is seen, not on
// later reloads, and again if it comes back after being fixed. A reference
// removed from the file alerts every time it is removed.
func TestModelRefAlerts_SkippedOncePerReferenceUntilFixed(t *testing.T) {
	rec := testalerts.Install(t)
	m := &modelRefAlerts{}
	alice := config.DanglingModelReference{Site: "agents.list[alice].models", Alias: "DeepSeek 4 Pro", Agent: "Alice"}
	defaults := config.DanglingModelReference{Site: "agents.defaults.image_model", Alias: "gone"}
	skipped := func(refs ...config.DanglingModelReference) modelRefPrune { return modelRefPrune{skipped: refs} }

	m.report(rec, skipped(alice))
	m.report(rec, skipped(alice))
	if n := len(rec.Alerts()); n != 1 {
		t.Fatalf("after a repeat: %d alerts, want 1", n)
	}
	m.report(rec, skipped(alice, defaults))
	if n := len(rec.Alerts()); n != 2 {
		t.Fatalf("after a new reference: %d alerts, want 2", n)
	}
	m.report(rec, modelRefPrune{}) // fixed
	m.report(rec, skipped(alice))
	got := rec.Alerts()
	if len(got) != 3 || got[2].EventID != "model-ref:agents.list[alice].models" {
		t.Fatalf("after fix and reappearance: %+v, want a third alert for Alice", got)
	}
	want := `Alice lists model "DeepSeek 4 Pro", which is missing or unusable; it was skipped and the next model in the list is used. Pick a model for Alice on the Agents page to clear this.`
	if got[0].Description != want {
		t.Fatalf("description = %q, want %q", got[0].Description, want)
	}
	if got[1].EventID != "model-ref:agents.defaults.image_model" {
		t.Fatalf("defaults alert EventID = %q", got[1].EventID)
	}

	m.report(rec, modelRefPrune{removed: []config.DanglingModelReference{alice, defaults}})
	got = rec.Alerts()
	if len(got) != 5 {
		t.Fatalf("after a removal: %d alerts, want 5", len(got))
	}
	if got[3].Description != aliceRemovedDesc {
		t.Fatalf("removed description = %q, want %q", got[3].Description, aliceRemovedDesc)
	}
	wantSite := `agents.defaults.image_model named model "gone", which no longer exists; it was removed from agents.defaults.image_model. Nothing else to do — choose an existing model there if you want one.`
	if got[4].Description != wantSite {
		t.Fatalf("removed site description = %q, want %q", got[4].Description, wantSite)
	}
}
