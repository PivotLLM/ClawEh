package tools

import "sync"

var (
	providerMu      sync.RWMutex
	globalProviders []ToolProvider
)

// RegisterProvider adds a provider to the global registry.
// Called from gateway wiring code (tool_providers.go) before agent loop starts.
func RegisterProvider(p ToolProvider) {
	providerMu.Lock()
	defer providerMu.Unlock()
	globalProviders = append(globalProviders, p)
}

// GetProviders returns all registered providers.
func GetProviders() []ToolProvider {
	providerMu.RLock()
	defer providerMu.RUnlock()
	result := make([]ToolProvider, len(globalProviders))
	copy(result, globalProviders)
	return result
}
