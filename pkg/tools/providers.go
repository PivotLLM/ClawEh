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
// ToolProvider (e.g. discovery tools registered by the MCP layer).
// These are included when building the default agent allowlist and the GUI
// catalog alongside provider-owned tools.
var StaticToolDescriptors = []ToolDescriptor{
	{Name: "find_tools_regex", Description: "Discover hidden MCP tools by regex search when tool discovery is enabled.", Category: "discovery", ConfigKey: "mcp.discovery.use_regex", DefaultEnabled: true},
	{Name: "find_tools_bm25", Description: "Discover hidden MCP tools by semantic ranking when tool discovery is enabled.", Category: "discovery", ConfigKey: "mcp.discovery.use_bm25", DefaultEnabled: true},
}

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
