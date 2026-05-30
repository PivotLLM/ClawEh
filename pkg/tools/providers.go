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
