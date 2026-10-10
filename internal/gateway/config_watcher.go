package gateway

import (
	"os"
	"sync"
	"time"

	"github.com/tenebris-tech/alerter"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/logger"
)

// setupFileChangeWatcher polls a single file and emits on the returned channel
// whenever its mtime or size changes. Intended for small, atomically-written
// state files (no debounce). The first observed state is the baseline.
func setupFileChangeWatcher(path string, interval time.Duration) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	stop := make(chan struct{})
	go func() {
		lastMod := getFileModTime(path)
		lastSize := getFileSize(path)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				m, s := getFileModTime(path), getFileSize(path)
				if m.After(lastMod) || s != lastSize {
					lastMod, lastSize = m, s
					select {
					case ch <- struct{}{}:
					default:
					}
				}
			case <-stop:
				return
			}
		}
	}()
	return ch, func() { close(stop) }
}

// setupConfigWatcherPolling sets up a simple polling-based watcher on the
// store's config file; a change is re-read into the store and a pruned copy
// (runtimeConfig) is emitted for the reload. A reference to a model that does
// not exist does not reject the reload: runtimeConfig removes it from the file
// and refAlerts raises one alert for it; the watcher takes that rewrite as
// already applied, so it does not reload for it again. interval controls how often the
// file is polled; callers should pass cfg.ConfigReloadInterval() so the value
// honours the config override and MinConfigReloadIntervalSeconds floor.
// Returns a channel for config updates, a stop function and markApplied.
//
// markApplied is for a reload applied outside the watcher (the force-reload
// API): given the file state that reload read (configFileStateOf, taken
// before reading), the watcher takes it as applied and drops a reload it has
// already queued, which would apply the same change a second time. A later
// change to the file still reloads. It returns once the watcher has done so.
// The queue is drained safely because the caller is the queue's only reader.
func setupConfigWatcherPolling(store *config.Store, interval, debounce time.Duration, debug bool, a alerter.Alerter, refAlerts *modelRefAlerts) (chan *config.Config, func(), func(configFileState)) {
	w := &configWatcher{
		store:     store,
		path:      store.Path(),
		interval:  interval,
		debounce:  debounce,
		debug:     debug,
		alerter:   a,
		refAlerts: refAlerts,
		out:       make(chan *config.Config, 1),
		marks:     make(chan configMark),
		stop:      make(chan struct{}),
	}
	w.wg.Go(w.run)
	return w.out, w.close, w.markApplied
}

// configWatcher polls the config file and hands each settled, valid change
// to the reload consumer. Everything but its channels is owned by the run
// goroutine.
type configWatcher struct {
	store     *config.Store
	path      string
	interval  time.Duration
	debounce  time.Duration
	debug     bool
	alerter   alerter.Alerter
	refAlerts *modelRefAlerts

	out   chan *config.Config
	marks chan configMark
	stop  chan struct{}
	wg    sync.WaitGroup

	// applied is the file state last reloaded; observed is the most recent
	// state seen on disk.
	applied  configFileState
	observed configFileState
	// Quiescence debounce: once a change is seen the watcher waits for the
	// file to be stable for debounce (reset by every further change) before
	// reloading, so a burst of edits collapses into one reload.
	pending       bool
	quietDeadline time.Time
}

// configMark is a markApplied request.
type configMark struct {
	applied configFileState
	done    chan struct{}
}

// run is the watcher's goroutine.
func (w *configWatcher) run() {
	w.applied = configFileStateOf(w.path)
	w.observed = w.applied
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			w.poll()
		case m := <-w.marks:
			w.applyMark(m)
		case <-w.stop:
			return
		}
	}
}

// poll looks at the file once and reloads it when a change has settled and
// differs from what was last applied.
func (w *configWatcher) poll() {
	current := configFileStateOf(w.path)

	// A change relative to the most recent observation resets the quiet
	// timer: the user is still editing.
	if current.modTime.After(w.observed.modTime) || current.size != w.observed.size {
		w.observed = current
		w.quietDeadline = time.Now().Add(w.debounce)
		w.pending = true
		if w.debug {
			logger.Debugf("🔍 Config file change detected; debouncing %s", w.debounce)
		}
		return
	}

	if !w.pending || time.Now().Before(w.quietDeadline) {
		return
	}
	w.pending = false
	if !current.modTime.After(w.applied.modTime) && current.size == w.applied.size {
		return
	}
	w.tryReload(current)
}

// tryReload reads the file into the store, validates the runtime copy and
// hands it to the reload consumer. A file that cannot be applied keeps the
// running configuration and raises an alert.
func (w *configWatcher) tryReload(current configFileState) {
	if _, err := w.store.Reload(); err != nil {
		logger.Errorf("⚠ Error loading new config: %v", err)
		logger.Warn("  Using previous valid config")
		alertConfigFileInvalid(w.alerter, w.path, err)
		return
	}
	newCfg, dangling, err := runtimeConfig(w.store)
	if err != nil {
		logger.Errorf("⚠ Error copying new config: %v", err)
		logger.Warn("  Using previous valid config")
		return
	}
	if dangling.persisted() {
		// runtimeConfig just rewrote the file to drop missing-model
		// references. What it wrote is what is being applied, so take it as
		// the baseline: our own write must not trigger another reload.
		current = configFileStateOf(w.path)
		w.observed = current
	}
	if err := newCfg.ValidateModels(); err != nil {
		logger.Errorf("  ⚠ New config validation failed: %v", err)
		logger.Warn("  Using previous valid config")
		alertConfigFileInvalid(w.alerter, w.path, err)
		return
	}
	if err := newCfg.ValidateBindings(); err != nil {
		logger.Errorf("  ⚠ New config binding validation failed: %v", err)
		logger.Warn("  Using previous valid config")
		alertConfigFileInvalid(w.alerter, w.path, err)
		return
	}
	w.refAlerts.report(w.alerter, dangling)

	// The change counts as applied only once the reload consumer has it.
	// While the consumer is still busy with a previous reload the watcher
	// re-arms and retries on a later tick; advancing the applied marker would
	// lose this change until the next edit or a restart.
	select {
	case w.out <- newCfg:
		w.applied = current
		logger.Info("✓ Config file validated and loaded")
	default:
		w.pending = true
		w.quietDeadline = time.Now() // retry on the next poll tick
		logger.Warn("⚠ Previous config reload still in progress, will retry")
	}
}

// applyMark takes the file state a force-reload applied as the baseline and
// drops a reload queued meanwhile, so the same change is not applied twice.
// A file that has changed since differs from the baseline and reloads after
// the debounce.
func (w *configWatcher) applyMark(m configMark) {
	w.applied = m.applied
	w.observed = m.applied
	w.pending = false
	select {
	case <-w.out:
		logger.Info("Dropped a queued config reload: the forced reload already applied it")
	default:
	}
	close(m.done)
}

// markApplied hands applied to the watcher and returns once it is taken.
func (w *configWatcher) markApplied(applied configFileState) {
	m := configMark{applied: applied, done: make(chan struct{})}
	select {
	case w.marks <- m:
		<-m.done
	case <-w.stop:
	}
}

// close stops the watcher and waits for its goroutine.
func (w *configWatcher) close() {
	close(w.stop)
	w.wg.Wait()
}

// configFileState identifies a version of the config file for the watcher.
type configFileState struct {
	modTime time.Time
	size    int64
}

// configFileStateOf returns the current state of the file at path.
func configFileStateOf(path string) configFileState {
	return configFileState{modTime: getFileModTime(path), size: getFileSize(path)}
}

// getFileModTime returns the modification time of a file, or zero time if file doesn't exist
func getFileModTime(path string) time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

// getFileSize returns the size of a file, or 0 if file doesn't exist
func getFileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

// alertConfigFileInvalid reports a config file the watcher could not apply.
// The running configuration is unchanged, but the next restart will fail on
// this file, so it needs a person now.
func alertConfigFileInvalid(a alerter.Alerter, configPath string, err error) {
	a.Send(alerter.Alert{
		Title:       "Config file invalid",
		Description: configPath + " was not applied; the running configuration is unchanged, but a restart will fail on it",
		Details:     err.Error(),
		EventID:     "config",
	})
}
