// ClawEh
// License: MIT

package gateway

import (
	"fmt"
	"sync"

	"github.com/tenebris-tech/alerter"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/logger"
)

// modelRefPrune is what one runtimeConfig pass did about references to models
// that do not exist.
type modelRefPrune struct {
	// removed were deleted from config.json through the store.
	removed []config.DanglingModelReference
	// skipped were dropped from the running copy only: the file could not be
	// written, or the model is defined but PruneInvalid dropped it (a bad
	// provider), which the operator repairs rather than losing the reference.
	skipped []config.DanglingModelReference
}

// persisted reports whether this pass rewrote config.json.
func (p modelRefPrune) persisted() bool { return len(p.removed) > 0 }

// pruneStoredModelReferences removes every reference to a model that is not
// defined in config.json from the store and the file, atomically, through
// Store.Update. Only dangling model references are persisted this way; invalid
// provider and model entries stay in the file (PruneInvalid is runtime-only by
// design, so they can be repaired). Nothing is written when there is nothing to
// remove, so a reload after our own write is a no-op. On a write error nothing
// is removed and the error is returned.
func pruneStoredModelReferences(store *config.Store) ([]config.DanglingModelReference, error) {
	var removed []config.DanglingModelReference
	err := store.Update(func(c *config.Config) error {
		removed = c.PruneDanglingModelReferences()
		if len(removed) == 0 {
			return config.ErrUnchanged
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, ref := range removed {
		logger.WarnCF("gateway", "removed reference to unknown model from config file", map[string]any{"site": ref.Site, "model": ref.Alias})
	}
	return removed, nil
}

// pruneModelReferences drops every reference to a model that does not exist
// from the runtime copy cfg (one the file write could not remove, or one to a
// model PruneInvalid just dropped for a bad provider), so no agent sends a dead
// alias as a model id: a list falls through to its next model. Each removal is
// logged, and so is each reference to a disabled model, which is kept. The
// on-disk file is not touched. It returns what was removed.
func pruneModelReferences(cfg *config.Config) []config.DanglingModelReference {
	removed := cfg.PruneDanglingModelReferences()
	for _, ref := range removed {
		logger.WarnCF("gateway", "skipped reference to unknown model", map[string]any{"site": ref.Site, "model": ref.Alias})
	}
	_, warnings := cfg.ValidateModelReferences()
	for _, w := range warnings {
		logger.WarnCF("gateway", "reference to disabled model", map[string]any{"detail": w})
	}
	return removed
}

// modelRefAlerts raises the operator alerts for missing-model references. A
// reference removed from config.json alerts every time it is removed: it is
// gone from the file afterwards, so seeing it again means it was put back. A
// reference only skipped at runtime is still in the file and is seen again on
// every reload, so it alerts once per process and is forgotten when it goes
// away (fixed by the operator), alerting again if it returns. Safe for
// concurrent use (boot, the config watcher and the forced reload).
type modelRefAlerts struct {
	mu      sync.Mutex
	alerted map[config.DanglingModelReference]bool
}

// report alerts on every reference in p.removed, and on every reference in
// p.skipped not already alerted, which becomes the set of skipped references
// currently known.
func (m *modelRefAlerts) report(a alerter.Alerter, p modelRefPrune) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, ref := range p.removed {
		a.Send(modelRefAlert(ref, true))
	}
	current := make(map[config.DanglingModelReference]bool, len(p.skipped))
	for _, ref := range p.skipped {
		current[ref] = true
		if !m.alerted[ref] {
			a.Send(modelRefAlert(ref, false))
		}
	}
	m.alerted = current
}

// modelRefAlert is the alert for one missing-model reference; persisted says
// whether it was removed from config.json or only skipped at runtime.
func modelRefAlert(ref config.DanglingModelReference, persisted bool) alerter.Alert {
	var desc string
	switch {
	case persisted && ref.Agent != "":
		desc = fmt.Sprintf("%s listed model %q, which no longer exists; it was removed from %s's model list and the next model in the list is now used. "+
			"Nothing else to do — check %s's models on the Agents page if you want a different one.", ref.Agent, ref.Alias, ref.Agent, ref.Agent)
	case persisted:
		desc = fmt.Sprintf("%s named model %q, which no longer exists; it was removed from %s. "+
			"Nothing else to do — choose an existing model there if you want one.", ref.Site, ref.Alias, ref.Site)
	case ref.Agent != "":
		desc = fmt.Sprintf("%s lists model %q, which is missing or unusable; it was skipped and the next model in the list is used. "+
			"Pick a model for %s on the Agents page to clear this.", ref.Agent, ref.Alias, ref.Agent)
	default:
		desc = fmt.Sprintf("%s names model %q, which is missing or unusable; it was skipped. "+
			"Choose an existing model there to clear this.", ref.Site, ref.Alias)
	}
	return alerter.Alert{
		Title:       "Agent references a missing model",
		Description: desc,
		Details:     fmt.Sprintf("%s: model %q does not exist", ref.Site, ref.Alias),
		EventID:     "model-ref:" + ref.Site,
	}
}
