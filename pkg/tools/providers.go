package tools

import "sync"

var (
	providerMu      sync.RWMutex
	globalProviders []ToolProvider
)

// RegisterProvider adds a provider to the global registry.
// Called from gateway wiring code (tool_providers.go) before agent loop starts.
// Idempotent: a provider with the same Namespace() is never added twice.
func RegisterProvider(p ToolProvider) {
	providerMu.Lock()
	defer providerMu.Unlock()
	for _, existing := range globalProviders {
		if existing.Namespace() == p.Namespace() {
			return // already registered
		}
	}
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

// StaticToolDescriptors holds descriptors for tools not owned by any
// ToolProvider. The progressive-discovery meta-tools (search_tools /
// get_tool_details) are intentionally NOT listed: they are not per-tool
// toggleable, they are registered as a unit whenever an agent's
// progressive_discovery is on and suppressed otherwise.
var StaticToolDescriptors = []ToolDescriptor{}

// DefaultEnabledToolNames returns the names of every registered provider-owned
// tool plus static tool whose descriptor is marked DefaultEnabled. It is the
// single source of truth for "tools on by default": it drives both the default
// per-agent allowlist and the default MCP-host allowlist, so marking a tool
// DefaultEnabled is sufficient to expose it everywhere without touching a
// hand-maintained list. Must be called after providers are registered.
func DefaultEnabledToolNames() []string {
	var names []string
	for _, p := range GetProviders() {
		for _, d := range p.Describe() {
			if d.DefaultEnabled {
				names = append(names, d.Name)
			}
		}
	}
	for _, d := range StaticToolDescriptors {
		if d.DefaultEnabled {
			names = append(names, d.Name)
		}
	}
	return names
}
