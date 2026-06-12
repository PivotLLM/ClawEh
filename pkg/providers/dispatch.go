// ClawEh - Personal AI Assistant
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package providers

import (
	"fmt"
	"strings"
	"sync"

	"github.com/PivotLLM/ClawEh/pkg/config"
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
		return nil, fmt.Errorf("dispatcher: empty alias")
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
