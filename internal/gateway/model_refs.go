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

// pruneModelReferences drops every reference to a model that does not exist
// from the runtime copy cfg (a deleted model, or one PruneInvalid just dropped
// for a bad provider), so no agent sends a dead alias as a model id: a list
// falls through to its next model. Each removal is logged, and so is each
// reference to a disabled model, which is kept. The on-disk file is not
// touched. It returns what was removed, for modelRefAlerts.
func pruneModelReferences(cfg *config.Config) []config.DanglingModelReference {
	removed := cfg.PruneDanglingModelReferences()
	for _, ref := range removed {
		logger.WarnCF("gateway", "removed reference to unknown model", map[string]any{"site": ref.Site, "model": ref.Alias})
	}
	_, warnings := cfg.ValidateModelReferences()
	for _, w := range warnings {
		logger.WarnCF("gateway", "reference to disabled model", map[string]any{"detail": w})
	}
	return removed
}

// modelRefAlerts raises one operator alert per missing-model reference the
// runtime skips, once per process: the config is re-read on every reload, and
// a reference that is still there must not alert again. A reference that goes
// away (the operator fixed it) is forgotten, so it alerts again if it returns.
// Safe for concurrent use (boot, the config watcher and the forced reload).
type modelRefAlerts struct {
	mu      sync.Mutex
	alerted map[config.DanglingModelReference]bool
}

// report alerts on every reference in removed not already alerted, and makes
// removed the set of references currently known to be missing.
func (m *modelRefAlerts) report(a alerter.Alerter, removed []config.DanglingModelReference) {
	m.mu.Lock()
	defer m.mu.Unlock()
	current := make(map[config.DanglingModelReference]bool, len(removed))
	for _, ref := range removed {
		current[ref] = true
		if !m.alerted[ref] {
			a.Send(modelRefAlert(ref))
		}
	}
	m.alerted = current
}

// modelRefAlert is the alert for one skipped missing-model reference.
func modelRefAlert(ref config.DanglingModelReference) alerter.Alert {
	var desc string
	if ref.Agent != "" {
		desc = fmt.Sprintf("%s lists model %q, which no longer exists; it was skipped and the next model in the list is used. "+
			"Pick a model for %s on the Agents page to clear this.", ref.Agent, ref.Alias, ref.Agent)
	} else {
		desc = fmt.Sprintf("%s names model %q, which no longer exists; it was skipped. "+
			"Choose an existing model there to clear this.", ref.Site, ref.Alias)
	}
	return alerter.Alert{
		Title:       "Agent references a missing model",
		Description: desc,
		Details:     fmt.Sprintf("%s: model %q does not exist", ref.Site, ref.Alias),
		EventID:     "model-ref:" + ref.Site,
	}
}
