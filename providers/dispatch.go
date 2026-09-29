// ClawEh
// License: MIT

package providers

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/tenebris-tech/alerter"

	"github.com/PivotLLM/ClawEh/alerts"
	"github.com/PivotLLM/ClawEh/config"
)

// ProviderDispatcher creates and caches per-model LLMProvider instances.
//
// The cache key is the user-facing alias (ModelConfig.ModelName). Keying by
// alias is required because multiple models entries may share the same raw
// model id while differing on per-entry state (response_log_file,
// reasoning_effort, extra_body, max_tokens_field, request_timeout, drop_params)
// or on the named provider they resolve through.
//
// Thread-safe: uses sync.RWMutex with read-locking for cache hits.
type ProviderDispatcher struct {
	mu    sync.RWMutex
	cache map[string]LLMProvider
	cfg   *config.Config
}

// NewProviderDispatcher creates a new dispatcher with the given config.
func NewProviderDispatcher(cfg *config.Config) *ProviderDispatcher {
	logBypassEnabled(cfg)
	return &ProviderDispatcher{
		cache: make(map[string]LLMProvider),
		cfg:   cfg,
	}
}

// Get returns a cached or newly created provider for the given model alias
// (ModelConfig.ModelName). The matching entry's provider reference is resolved
// to a configured provider, which supplies the wire protocol, base URL, and
// credentials.
//
// Returns an error if no enabled ModelConfig matches the alias, the model's
// provider cannot be resolved, or provider creation fails.
func (d *ProviderDispatcher) Get(alias string) (LLMProvider, error) {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return nil, errors.New("dispatcher: empty alias")
	}

	// Fast path: read-lock to check cache.
	d.mu.RLock()
	if p, ok := d.cache[alias]; ok {
		d.mu.RUnlock()
		return p, nil
	}
	d.mu.RUnlock()

	// Slow path: find config outside any lock, then store under write-lock.
	d.mu.RLock()
	cfgSnapshot := d.cfg
	var matched *config.ModelConfig
	for i := range cfgSnapshot.Models {
		if !cfgSnapshot.Models[i].Enabled {
			continue
		}
		if cfgSnapshot.Models[i].ModelName == alias {
			cp := cfgSnapshot.Models[i]
			matched = &cp
			break
		}
	}
	// A model with no workspace runs its CLI in <CLAW_HOME>/cli.
	if matched != nil && matched.Workspace == "" && cfgSnapshot.DataDir() != "" {
		matched.Workspace = cfgSnapshot.CLIPath()
	}
	if matched != nil && matched.RequestTimeout == 0 && cfgSnapshot.Agents.Defaults.RequestTimeout > 0 {
		matched.RequestTimeout = cfgSnapshot.Agents.Defaults.RequestTimeout
	}
	d.mu.RUnlock()

	if matched == nil {
		return nil, fmt.Errorf("dispatcher: no enabled models entry with model_name=%q", alias)
	}

	prov, err := cfgSnapshot.GetProvider(matched.Provider)
	if err != nil {
		return nil, fmt.Errorf("dispatcher: resolving provider for %q: %w", alias, err)
	}

	// Create the provider outside any lock (may do I/O).
	provider, _, err := CreateProviderFromConfig(matched, prov)
	if err != nil {
		return nil, fmt.Errorf("dispatcher: creating provider for %q: %w", alias, err)
	}
	alertBypassIgnored(cfgSnapshot, matched, prov)

	// Write-lock only to store; double-check in case another goroutine raced us.
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cfg != cfgSnapshot {
		// Config was reloaded while we created the provider; discard it.
		return nil, fmt.Errorf("dispatcher: config reloaded during provider creation for %q, retry", alias)
	}
	if p, ok := d.cache[alias]; ok {
		return p, nil
	}
	d.cache[alias] = provider
	return provider, nil
}

// Flush clears the provider cache and updates the config reference.
// Call this after a config reload so the dispatcher picks up new settings.
func (d *ProviderDispatcher) Flush(cfg *config.Config) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cache = make(map[string]LLMProvider)
	d.cfg = cfg
}

// bypassIgnoredAlerted holds the model aliases already alerted for a dropped
// bypass flag: once per model per process.
var bypassIgnoredAlerted sync.Map

// alertBypassIgnored raises one alert when a CLI model's extra_args still
// carries the CLI's permission-bypass flag while the provider's "Allow CLI to
// bypass restrictions" is off, so the flag is dropped (config.CLIArgs). It names
// the agents that use the model, since that is who loses the behaviour, and the
// model, since that is where the flag sits.
func alertBypassIgnored(cfg *config.Config, m *config.ModelConfig, prov *config.Provider) {
	if cfg == nil || m == nil || prov == nil || prov.BypassRestrictions {
		return
	}
	cli := config.CLIAgentByProtocol(prov.Protocol)
	if cli == nil {
		return
	}
	var flags []string
	for _, a := range m.ExtraArgs {
		if slices.Contains(cli.BypassArgs, a) {
			flags = append(flags, a)
		}
	}
	if len(flags) == 0 {
		return
	}
	if _, dup := bypassIgnoredAlerted.LoadOrStore(strings.ToLower(m.ModelName), true); dup {
		return
	}
	who := strings.Join(agentsUsingModel(cfg, m.ModelName), ", ")
	if who == "" {
		who = "No agent"
	}
	alerts.Send(alerter.Alert{
		Title:       who + ": bypass flag ignored for " + m.ModelName,
		Description: m.ModelName + " lists " + strings.Join(flags, " ") + ", but Allow CLI to bypass restrictions is off for " + prov.Name + ", so it is not passed. Tick it on the Providers page.",
		EventID:     "bypass:" + m.ModelName,
	})
}

// agentsUsingModel lists the enabled agents whose model chain names the alias:
// their own list when they have one, the defaults otherwise. Display names
// where set, ids otherwise, in config order.
func agentsUsingModel(cfg *config.Config, alias string) []string {
	uses := func(list []string) bool {
		for _, m := range list {
			if strings.EqualFold(strings.TrimSpace(m), alias) {
				return true
			}
		}
		return false
	}
	var out []string
	for i := range cfg.Agents.List {
		a := &cfg.Agents.List[i]
		if !a.IsEnabled() {
			continue
		}
		chain := a.Models
		if len(chain) == 0 {
			chain = cfg.Agents.Defaults.Models
		}
		if uses(chain) {
			name := a.Name
			if name == "" {
				name = a.ID
			}
			out = append(out, name)
		}
	}
	return out
}
