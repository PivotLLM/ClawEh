package gateway

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tenebris-tech/alerter"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/internal/clock"
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

// The watcher tests run it on a fake clock: a tick polls the file once and
// returns when that poll is done, so every step is exact.
const (
	testPollInterval = time.Second
	testDebounce     = 5 * time.Second
	// debounceTicks is how many polls after the last change the reload
	// happens: the first poll at or after the quiet deadline.
	debounceTicks = int(testDebounce / testPollInterval)
)

// watcherHarness drives a configWatcher on a fake clock.
type watcherHarness struct {
	w      *configWatcher
	fc     *clock.Fake
	polled chan struct{}
	ch     <-chan *config.Config
}

// startWatcher runs a watcher on store's file and returns once it has taken
// the file as its baseline.
func startWatcher(t *testing.T, store *config.Store, a alerter.Alerter) *watcherHarness {
	t.Helper()
	h := &watcherHarness{fc: clock.NewFake(time.Now()), polled: make(chan struct{})}
	h.w = newConfigWatcher(store, testPollInterval, testDebounce, false, a, &modelRefAlerts{})
	h.w.clock = h.fc
	h.w.polled = h.polled
	h.ch = h.w.out
	h.w.start()
	t.Cleanup(h.w.close)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := h.fc.BlockUntil(ctx, 1); err != nil { // its ticker: the baseline is taken
		t.Fatalf("the watcher never started: %v", err)
	}
	return h
}

// tick moves the clock one poll interval on and waits for that poll.
func (h *watcherHarness) tick(t *testing.T) {
	t.Helper()
	h.fc.Advance(testPollInterval)
	select {
	case <-h.polled:
	case <-time.After(10 * time.Second):
		t.Fatal("the watcher did not poll")
	}
}

// ticks is n ticks.
func (h *watcherHarness) ticks(t *testing.T, n int) {
	t.Helper()
	for range n {
		h.tick(t)
	}
}

// noReload fails the test when a reload is queued.
func (h *watcherHarness) noReload(t *testing.T, why string) {
	t.Helper()
	if len(h.ch) != 0 {
		t.Fatal(why)
	}
}

// settle polls until a change written now has been reloaded, failing when
// it is reloaded early or not at all: the reload comes exactly debounceTicks
// polls after the poll that saw it.
func (h *watcherHarness) settle(t *testing.T) *config.Config {
	t.Helper()
	h.tick(t) // sees the change
	for range debounceTicks {
		h.noReload(t, "reloaded before the file had been quiet for the debounce")
		h.tick(t)
	}
	select {
	case cfg := <-h.ch:
		return cfg
	default:
		t.Fatal("expected a reload once the file had been quiet for the debounce; got none")
		return nil
	}
}

// TestConfigWatcher_DebouncesBurstIntoSingleReload verifies that a burst of
// writes within the debounce window collapses into exactly one reload, and that
// each write resets the quiet timer (no reload until the file goes quiet).
func TestConfigWatcher_DebouncesBurstIntoSingleReload(t *testing.T) {
	store, path := seedStore(t)
	h := startWatcher(t, store, alerter.Nop{})

	// A write before every poll for twice the debounce: each one resets the
	// quiet timer, so nothing may be reloaded during the burst.
	for i := range 2 * debounceTicks {
		// A different length every time, so the change is seen even where the
		// file system's modification times are coarse.
		writeConfig(t, path, strings.Repeat("x", i+1))
		h.tick(t)
		h.noReload(t, "reload fired during the burst; debounce did not reset the timer")
	}

	// The last write was seen; exactly debounceTicks quiet polls later the
	// one reload arrives.
	for range debounceTicks - 1 {
		h.tick(t)
		h.noReload(t, "reloaded before the file had been quiet for the debounce")
	}
	h.tick(t)
	select {
	case <-h.ch:
	default:
		t.Fatal("expected one reload after the file went quiet; got none")
	}

	// And no second reload for the same settled state.
	h.ticks(t, 2*debounceTicks)
	h.noReload(t, "unexpected second reload for an unchanged file")
}

// TestConfigWatcher_MarkAppliedSuppressesReload verifies that after a change is
// written and markApplied() is called (as the force-reload path does), the
// watcher advances its baseline and does NOT fire a redundant reload — the
// double-reload that was tearing down an active chat after the setup wizard.
func TestConfigWatcher_MarkAppliedSuppressesReload(t *testing.T) {
	store, path := seedStore(t)
	h := startWatcher(t, store, alerter.Nop{})

	// Simulate a force-reload: the config changes and the out-of-band path
	// applies it, then tells the watcher via markApplied().
	writeConfig(t, path, "force-applied")
	h.w.markApplied(configFileStateOf(path))

	// The watcher must not deliver a reload for the already-applied change.
	h.ticks(t, 2*debounceTicks)
	h.noReload(t, "watcher fired a redundant reload after markApplied()")

	// A genuinely new change after markApplied still triggers a reload.
	writeConfig(t, path, "later-edit")
	h.settle(t)
}

// TestConfigWatcher_MarkAppliedDropsQueuedReload: a forced reload can take
// longer than the watcher's debounce, so the watcher may already have queued
// a reload for the same change by the time markApplied is called. That reload
// is dropped; it would apply the same change a second time.
func TestConfigWatcher_MarkAppliedDropsQueuedReload(t *testing.T) {
	store, path := seedStore(t)
	h := startWatcher(t, store, alerter.Nop{})

	writeConfig(t, path, "force-applied")
	applied := configFileStateOf(path) // what the forced reload reads
	h.ticks(t, debounceTicks+1)
	if len(h.ch) != 1 {
		t.Fatal("the watcher did not queue a reload for the change")
	}
	h.w.markApplied(applied)
	h.noReload(t, "the reload queued for the applied change is still queued after markApplied")
	h.ticks(t, 2*debounceTicks)
	h.noReload(t, "watcher fired a redundant reload after markApplied()")

	writeConfig(t, path, "later-edit")
	h.settle(t)
}

// TestConfigWatcher_MarkAppliedKeepsLaterChange: a change written after the
// forced reload read the file is newer than the state it marks applied, so it
// is still reloaded even though markApplied dropped the queued reload.
func TestConfigWatcher_MarkAppliedKeepsLaterChange(t *testing.T) {
	store, path := seedStore(t)
	h := startWatcher(t, store, alerter.Nop{})

	writeConfig(t, path, "force-applied")
	applied := configFileStateOf(path)
	writeConfig(t, path, "written while the forced reload ran, longer")
	h.ticks(t, debounceTicks+1)
	if len(h.ch) != 1 {
		t.Fatal("the watcher did not queue a reload for the change")
	}
	h.w.markApplied(applied)

	// The queued reload was dropped, but the file differs from what was
	// marked applied, so it is seen as a change and reloaded.
	if got := h.settle(t); got == nil {
		t.Fatal("nil config reloaded")
	}
}

// TestConfigWatcher_RetriesWhenConsumerBusy verifies that a change which cannot
// be delivered because the consumer is still draining a previous reload is NOT
// lost: once the consumer catches up, the pending change is delivered. This
// guards the bug where the applied marker advanced on a dropped send, so
// enabling an agent's tool suite silently required a restart.
func TestConfigWatcher_RetriesWhenConsumerBusy(t *testing.T) {
	store, path := seedStore(t)
	h := startWatcher(t, store, alerter.Nop{})

	// First change A is delivered into the cap-1 buffer; we intentionally do NOT
	// read it yet, simulating a consumer still busy with a prior reload.
	writeConfig(t, path, "A")
	h.ticks(t, debounceTicks+1)
	if len(h.ch) != 1 {
		t.Fatal("first reload (A) was never delivered to the buffer")
	}

	// Second change B settles while the buffer is full, so its send is
	// dropped; the watcher keeps retrying on every poll instead of advancing
	// the applied marker.
	writeConfig(t, path, "BB")
	h.ticks(t, 3*debounceTicks)

	// Drain A (consumer catches up).
	select {
	case <-h.ch:
	default:
		t.Fatal("expected the first reload (A) to be buffered")
	}

	// B must still be delivered, at the next poll: not lost.
	h.tick(t)
	select {
	case <-h.ch:
	default:
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

	h := startWatcher(t, store, rec)

	if err := os.WriteFile(path, []byte(danglingRefConfigJSON("first")), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := h.settle(t)

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
	h.ticks(t, 2*debounceTicks)
	h.noReload(t, "the watcher reloaded its own rewrite of the file")
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
	cfg = h.settle(t)
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
	h.settle(t)
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

	h := startWatcher(t, store, rec)

	body := `{"models":[],"agents":{"defaults":{"models":[]}},
		"bindings":[{"agent_id":"main","default":true,"match":{"channel":""}}]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	h.ticks(t, 2*debounceTicks)
	h.noReload(t, "a config with an invalid default binding was applied")
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

// A test that stops reading the polled hook cannot hold up close: the
// watcher gives up the send when it is stopped.
func TestConfigWatcher_UnreadPolledHookDoesNotHangClose(t *testing.T) {
	store, _ := seedStore(t)
	fc := clock.NewFake(time.Now())
	w := newConfigWatcher(store, testPollInterval, testDebounce, false, alerter.Nop{}, &modelRefAlerts{})
	w.clock = fc
	w.polled = make(chan struct{}) // never read
	w.start()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := fc.BlockUntil(ctx, 1); err != nil {
		t.Fatalf("the watcher never started: %v", err)
	}
	fc.Advance(testPollInterval)
	closed := make(chan struct{})
	go func() { w.close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		t.Fatal("close hung on the unread polled hook")
	}
}

// The service-token file watcher polls on the clock it is given, reports a
// change at the next poll, and its stop returns once its goroutine is done.
func TestFileChangeWatcher_OnTheClock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	fc := clock.NewFake(time.Now())
	changes, stop := setupFileChangeWatcher(fc, path, time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := fc.BlockUntil(ctx, 1); err != nil { // its ticker: the baseline is taken
		t.Fatalf("the watcher never started: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"a":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changes:
		t.Fatal("reported a change before the clock moved")
	default:
	}
	fc.Advance(time.Minute)
	select {
	case <-changes:
	case <-time.After(10 * time.Second):
		t.Fatal("the change was not reported after a poll")
	}
	stop()
	if n := fc.Waiters(); n != 0 {
		t.Fatalf("waiters = %d after stop: its goroutine was still running", n)
	}
}
