// PicoClaw - Ultra-lightweight personal AI agent
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package providers

import (
	"fmt"
	"strings"
	"sync"

	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/logger"
)

// ProviderDispatcher creates and caches per-model LLMProvider instances.
//
// The cache key is the user-facing alias (ModelConfig.ModelName), not the
// wire model (protocol+"/"+modelID). Keying by alias is required because
// multiple model_list entries may share the same wire model while differing
// on per-entry openai_compat state (response_log_file, reasoning_effort,
// extra_body, strict_compat, no_parallel_tool_calls, max_tokens_field,
// request_timeout, proxy, api_key, model_label). Keying by wire model
// shadowed all entries past the first-match.
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
// (ModelConfig.ModelName).
//
// Lookup precedence:
//  1. enabled ModelList entry with ModelName == alias (the intended path).
//  2. enabled ModelList entry whose wire model (protocol+"/"+modelID) equals
//     the alias — backwards-compatible fallback for callers that still hand
//     in a wire model. A DBG log fires so stragglers can be found and fixed.
//
// Returns an error if no matching ModelConfig is found or provider creation fails.
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
	var matchedByWire bool
	for i := range cfgSnapshot.ModelList {
		if !cfgSnapshot.ModelList[i].Enabled {
			continue
		}
		if cfgSnapshot.ModelList[i].ModelName == alias {
			cp := cfgSnapshot.ModelList[i]
			matched = &cp
			break
		}
	}
	if matched == nil {
		// Backwards-compatible fallback: match by wire model.
		for i := range cfgSnapshot.ModelList {
			if !cfgSnapshot.ModelList[i].Enabled {
				continue
			}
			p, m := ExtractProtocol(cfgSnapshot.ModelList[i].Model)
			if p+"/"+m == alias {
				cp := cfgSnapshot.ModelList[i]
				matched = &cp
				matchedByWire = true
				break
			}
		}
	}
	if matched != nil && matched.RequestTimeout == 0 && cfgSnapshot.Agents.Defaults.RequestTimeout > 0 {
		matched.RequestTimeout = cfgSnapshot.Agents.Defaults.RequestTimeout
	}
	d.mu.RUnlock()

	if matched == nil {
		return nil, fmt.Errorf("dispatcher: no enabled model_list entry with model_name=%q", alias)
	}

	if matchedByWire {
		logger.DebugCF("providers", "dispatcher: caller passed wire model instead of alias",
			map[string]any{
				"wire_model": alias,
				"model_name": matched.ModelName,
			})
	}

	// Create the provider outside any lock (may do I/O).
	provider, _, err := CreateProviderFromConfig(matched)
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
