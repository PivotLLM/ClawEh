// ClawEh
// License: MIT

package providers

import (
	"errors"
	"fmt"
	"strings"
	"sync"

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

// GetIsolated returns the provider for alias as a fresh temporary agent uses
// it. An HTTP model is the shared cached provider (Get). A CLI model is a
// provider of its own, never cached: it runs in workspace (the agent's own
// empty workspace, never the shared cli/ directory) and without the
// provider's permission-bypass flags whatever bypass_restrictions says, since
// a fresh agent has no tools to grant. It is not wrapped in the
// declined-tools guard, whose message points at the bypass setting.
func (d *ProviderDispatcher) GetIsolated(alias, workspace string) (LLMProvider, error) {
	alias = strings.TrimSpace(alias)
	if strings.TrimSpace(workspace) == "" {
		return nil, errors.New("dispatcher: isolated provider needs a workspace")
	}
	d.mu.RLock()
	cfgSnapshot := d.cfg
	d.mu.RUnlock()
	var matched *config.ModelConfig
	for i := range cfgSnapshot.Models {
		if cfgSnapshot.Models[i].Enabled && cfgSnapshot.Models[i].ModelName == alias {
			cp := cfgSnapshot.Models[i]
			matched = &cp
			break
		}
	}
	if matched == nil {
		return nil, fmt.Errorf("dispatcher: no enabled models entry with model_name=%q", alias)
	}
	prov, err := cfgSnapshot.GetProvider(matched.Provider)
	if err != nil {
		return nil, fmt.Errorf("dispatcher: resolving provider for %q: %w", alias, err)
	}
	if config.CLIAgentByProtocol(prov.Protocol) == nil {
		return d.Get(alias)
	}
	isolated := *prov
	isolated.BypassRestrictions = false
	matched.Workspace = workspace
	if matched.RequestTimeout == 0 && cfgSnapshot.Agents.Defaults.RequestTimeout > 0 {
		matched.RequestTimeout = cfgSnapshot.Agents.Defaults.RequestTimeout
	}
	p, _, err := CreateProviderFromConfig(matched, &isolated)
	if err != nil {
		return nil, fmt.Errorf("dispatcher: creating isolated provider for %q: %w", alias, err)
	}
	if g, ok := p.(*cliDeclinedGuard); ok {
		p = g.LLMProvider
	}
	return p, nil
}
