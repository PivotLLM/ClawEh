package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/logger"
	"github.com/PivotLLM/ClawEh/pkg/providers"
	"github.com/PivotLLM/ClawEh/pkg/utils"
)

type ToolEntry struct {
	Tool   Tool
	IsCore bool
	TTL    int
	// SuiteExempt marks a tool that belongs to an all-or-nothing suite (cogmem,
	// maestro). Suites are gated as a unit by the per-agent suite flag at
	// registration time, so their tools must also bypass the execution-time
	// per-tool allowlist check (which has no entry for them).
	SuiteExempt bool
	// promoteTTL records the TTL a hidden tool was last promoted with (via
	// PromoteTools). ExecuteWithContext resets TTL to this on each use so a
	// discovered tool doesn't get re-hidden mid-task. Zero until first promoted.
	promoteTTL int
}

type ToolRegistry struct {
	tools   map[string]*ToolEntry
	mu      sync.RWMutex
	version atomic.Uint64 // incremented on Register/RegisterHidden for cache invalidation
}

func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{
		tools: make(map[string]*ToolEntry),
	}
}

func (r *ToolRegistry) Register(tool Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	name := tool.Name()
	if _, exists := r.tools[name]; exists {
		logger.WarnCF("tools", "Tool registration overwrites existing tool",
			map[string]any{"name": name})
	}
	r.tools[name] = &ToolEntry{
		Tool:   tool,
		IsCore: true,
		TTL:    0, // Core tools do not use TTL
	}
	r.version.Add(1)
	logger.DebugCF("tools", "Registered core tool", map[string]any{"name": name})
}

// RegisterSuite registers a tool that belongs to an all-or-nothing suite. It is
// identical to Register but marks the entry SuiteExempt, so ExecuteWithContext
// skips the per-tool allowlist check (the suite flag is the allow decision).
func (r *ToolRegistry) RegisterSuite(tool Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	name := tool.Name()
	if _, exists := r.tools[name]; exists {
		logger.WarnCF("tools", "Tool registration overwrites existing tool",
			map[string]any{"name": name})
	}
	r.tools[name] = &ToolEntry{
		Tool:        tool,
		IsCore:      true,
		TTL:         0,
		SuiteExempt: true,
	}
	r.version.Add(1)
	logger.DebugCF("tools", "Registered suite tool", map[string]any{"name": name})
}

// RegisterSuiteHidden registers a suite tool that is ALSO hidden behind
// progressive discovery: suite-exempt from the per-tool allowlist (the suite flag
// is the allow decision) yet TTL-gated like a hidden tool, so it stays out of the
// advertised set until search/get_tool_details promotes it. Used for the fusion
// and maestro suites when an agent has progressive discovery on.
func (r *ToolRegistry) RegisterSuiteHidden(tool Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	name := tool.Name()
	if _, exists := r.tools[name]; exists {
		logger.WarnCF("tools", "Tool registration overwrites existing tool",
			map[string]any{"name": name})
	}
	r.tools[name] = &ToolEntry{
		Tool:        tool,
		IsCore:      false, // TTL-gated (hidden) until promoted
		TTL:         0,
		SuiteExempt: true, // gated by the suite flag, not the per-tool allowlist
	}
	r.version.Add(1)
	logger.DebugCF("tools", "Registered hidden suite tool", map[string]any{"name": name})
}

// RegisterHidden saves hidden tools (visible only via TTL)
func (r *ToolRegistry) RegisterHidden(tool Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	name := tool.Name()
	if _, exists := r.tools[name]; exists {
		logger.WarnCF("tools", "Hidden tool registration overwrites existing tool",
			map[string]any{"name": name})
	}
	r.tools[name] = &ToolEntry{
		Tool:   tool,
		IsCore: false,
		TTL:    0,
	}
	r.version.Add(1)
	logger.DebugCF("tools", "Registered hidden tool", map[string]any{"name": name})
}

// PromoteTools atomically sets the TTL for multiple non-core tools.
// This prevents a concurrent TickTTL from decrementing between promotions.
func (r *ToolRegistry) PromoteTools(names []string, ttl int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	promoted := 0
	for _, name := range names {
		if entry, exists := r.tools[name]; exists {
			if !entry.IsCore {
				entry.TTL = ttl
				entry.promoteTTL = ttl // baseline for TTL-reset-on-use
				promoted++
			}
		}
	}
	logger.DebugCF(
		"tools",
		"PromoteTools completed",
		map[string]any{"requested": len(names), "promoted": promoted, "ttl": ttl},
	)
}

// TickTTL decreases TTL only for non-core tools
func (r *ToolRegistry) TickTTL() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, entry := range r.tools {
		if !entry.IsCore && entry.TTL > 0 {
			entry.TTL--
		}
	}
}

// Version returns the current registry version (atomically).
func (r *ToolRegistry) Version() uint64 {
	return r.version.Load()
}

// HiddenToolSnapshot holds a consistent snapshot of hidden tools and the
// registry version at which it was taken. Used by BM25SearchTool cache.
type HiddenToolSnapshot struct {
	Docs    []HiddenToolDoc
	Version uint64
}

// HiddenToolDoc is a lightweight representation of a hidden tool for search indexing.
type HiddenToolDoc struct {
	Name        string
	Description string
}

// SnapshotHiddenTools returns all non-core tools and the current registry
// version under a single read-lock, guaranteeing consistency between the
// two values.
func (r *ToolRegistry) SnapshotHiddenTools() HiddenToolSnapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	docs := make([]HiddenToolDoc, 0, len(r.tools))
	for name, entry := range r.tools {
		if !entry.IsCore {
			docs = append(docs, HiddenToolDoc{
				// Advertise the model-facing name (bare ExternalName for MCP tools),
				// so search results name a tool the way it appears once unlocked.
				Name:        advertisedName(entry.Tool, name),
				Description: entry.Tool.Description(),
			})
		}
	}
	return HiddenToolSnapshot{
		Docs:    docs,
		Version: r.version.Load(),
	}
}

// HiddenNamespaces returns the sorted, distinct leading namespaces of the
// currently hidden tools (e.g. "trello", "maestro", "browser") — the groups a
// model can discover via search_tools. Used to hint the available surface without
// listing every tool.
func (r *ToolRegistry) HiddenNamespaces() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	set := make(map[string]struct{})
	for name, entry := range r.tools {
		if entry.IsCore {
			continue
		}
		adv := advertisedName(entry.Tool, name)
		ns := adv
		if i := strings.IndexByte(adv, '_'); i > 0 {
			ns = adv[:i]
		}
		set[ns] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for ns := range set {
		out = append(out, ns)
	}
	sort.Strings(out)
	return out
}

func (r *ToolRegistry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.tools[name]
	if !ok {
		return nil, false
	}
	// Hidden tools with expired TTL are not callable.
	if !entry.IsCore && entry.TTL <= 0 {
		return nil, false
	}
	return entry.Tool, true
}

// resolve maps a model-facing tool name to its registry entry. It first tries the
// internal registry key; on a miss it matches an MCP tool by its ExternalName —
// the bare "<server>_<tool>" form advertised to models (see ToProviderDefs) — so a
// model that calls the tool without claw's internal "mcp_" prefix still dispatches
// and gates correctly. Returns the entry, the internal (canonical) name to use for
// allow-list gating, and whether it resolved. Expired hidden tools do not resolve.
func (r *ToolRegistry) resolve(name string) (*ToolEntry, string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if entry, ok := r.tools[name]; ok {
		if !entry.IsCore && entry.TTL <= 0 {
			return nil, "", false
		}
		return entry, name, true
	}
	for internal, entry := range r.tools {
		if !entry.IsCore && entry.TTL <= 0 {
			continue
		}
		if en, ok := entry.Tool.(ExternalNamer); ok && en.ExternalName() == name {
			return entry, internal, true
		}
	}
	return nil, "", false
}

// advertisedName is the model-facing name for a tool: the bare ExternalName for
// MCP tools (matching ToProviderDefs), else the internal registry name.
func advertisedName(t Tool, internal string) string {
	if en, ok := t.(ExternalNamer); ok {
		if ext := en.ExternalName(); ext != "" {
			return ext
		}
	}
	return internal
}

// findAnyLocked returns the entry and internal name for a tool by internal key or
// ExternalName, IGNORING TTL so hidden (unpromoted) tools are found. Caller holds
// at least r.mu.RLock.
func (r *ToolRegistry) findAnyLocked(name string) (*ToolEntry, string) {
	if entry, ok := r.tools[name]; ok {
		return entry, name
	}
	for internal, entry := range r.tools {
		if en, ok := entry.Tool.(ExternalNamer); ok && en.ExternalName() == name {
			return entry, internal
		}
	}
	return nil, ""
}

// revealTool promotes a hidden tool by name (ignoring current TTL) and returns its
// provider schema, the model-facing (advertised) name, and the internal dispatch
// name. name may be the internal registry key or a bare ExternalName. ok=false if
// there is no such tool.
func (r *ToolRegistry) revealTool(name string, ttl int) (schema map[string]any, advertised, internal string, ok bool) {
	r.mu.Lock()
	entry, in := r.findAnyLocked(name)
	if entry == nil {
		r.mu.Unlock()
		return nil, "", "", false
	}
	if !entry.IsCore {
		entry.TTL = ttl
		entry.promoteTTL = ttl
	}
	tool := entry.Tool
	r.mu.Unlock()
	return ToolToSchema(tool), advertisedName(tool, in), in, true
}

// RevealTool is the exported form the MCP host uses to unlock a hidden tool for a
// session: it promotes the tool (so it becomes callable) and returns its schema,
// advertised name, and internal dispatch name.
func (r *ToolRegistry) RevealTool(name string, ttl int) (schema map[string]any, advertised, internal string, ok bool) {
	return r.revealTool(name, ttl)
}

func (r *ToolRegistry) Execute(ctx context.Context, name string, args map[string]any) *ToolResult {
	return r.ExecuteWithContext(ctx, name, args, "", "", nil)
}

// repromoteOnUse resets a discovered tool's TTL to its promotion baseline so
// active use keeps it visible instead of letting it decay mid-task. No-op for
// always-on (core) tools and tools that were never promoted.
func (r *ToolRegistry) repromoteOnUse(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if entry, ok := r.tools[name]; ok && !entry.IsCore && entry.promoteTTL > 0 {
		entry.TTL = entry.promoteTTL
	}
}

// ExecuteWithContext executes a tool with channel/chatID context and optional async callback.
// If the tool implements AsyncExecutor and a non-nil callback is provided,
// ExecuteAsync is called instead of Execute — the callback is a parameter,
// never stored as mutable state on the tool.
func (r *ToolRegistry) ExecuteWithContext(
	ctx context.Context,
	name string,
	args map[string]any,
	channel, chatID string,
	asyncCallback AsyncCallback,
) *ToolResult {
	// Tool arguments are intentionally NOT logged: they routinely contain memory
	// content, file contents, and other user data that must not leak into logs.
	logger.InfoCF("tool", "Tool execution started",
		map[string]any{
			"tool": name,
		})

	// Resolve first so the model may call an MCP tool by its bare ExternalName
	// (the name it is advertised under) as well as the internal registry key.
	entry, canonical, ok := r.resolve(name)
	if !ok {
		logger.ErrorCF("tool", "Tool not found",
			map[string]any{
				"tool": name,
			})
		return ErrorResult(fmt.Sprintf("tool %q not found", name)).WithError(fmt.Errorf("tool not found"))
	}

	// Defense-in-depth: check tool allowlist from context before execution.
	// Suite tools (cogmem, maestro) are exempt — they are gated as a unit by the
	// per-agent suite flag at registration, not by the per-tool allowlist. Gate on
	// the canonical (internal) name so MCP tools route to the mcp_tools allow-list
	// even when the caller used the bare ExternalName.
	if checker := ToolAllowCheckerFromCtx(ctx); checker != nil && !entry.SuiteExempt {
		if !checker.IsToolAllowed(canonical) {
			logger.WarnCF("tool", "Tool execution denied by agent allowlist",
				map[string]any{
					"tool": canonical,
				})
			return ErrorResult(fmt.Sprintf("tool not permitted: %s", name)).WithError(fmt.Errorf("tool not permitted: %s", name))
		}
	}

	tool := entry.Tool

	// Keep a discovered tool alive across a task: using it resets its TTL to the
	// promotion baseline so it isn't re-hidden mid-use. No-op for always-on tools.
	r.repromoteOnUse(canonical)

	// Inject channel/chatID into ctx so tools read them via ToolChannel(ctx)/ToolChatID(ctx).
	// Always inject — tools validate what they require.
	ctx = WithToolContext(ctx, channel, chatID)

	// If tool implements AsyncExecutor and callback is provided, use ExecuteAsync.
	// The callback is a call parameter, not mutable state on the tool instance.
	var result *ToolResult
	start := time.Now()
	if asyncExec, ok := tool.(AsyncExecutor); ok && asyncCallback != nil {
		logger.DebugCF("tool", "Executing async tool via ExecuteAsync",
			map[string]any{
				"tool": name,
			})
		result = asyncExec.ExecuteAsync(ctx, args, asyncCallback)
	} else {
		result = tool.Execute(ctx, args)
	}
	duration := time.Since(start)

	// Log based on result type
	if result.IsError {
		logger.ErrorCF("tool", "Tool execution failed",
			map[string]any{
				"tool":     name,
				"duration": duration.Milliseconds(),
				"error":    utils.Truncate(result.ForLLM, 500),
			})
	} else if result.Async {
		logger.InfoCF("tool", "Tool started (async)",
			map[string]any{
				"tool":     name,
				"duration": duration.Milliseconds(),
			})
	} else {
		logger.InfoCF("tool", "Tool execution completed",
			map[string]any{
				"tool":          name,
				"duration_ms":   duration.Milliseconds(),
				"result_length": len(result.ForLLM),
			})
	}

	return result
}

// sortedToolNames returns tool names in sorted order for deterministic iteration.
// This is critical for KV cache stability: non-deterministic map iteration would
// produce different system prompts and tool definitions on each call, invalidating
// the LLM's prefix cache even when no tools have changed.
func (r *ToolRegistry) sortedToolNames() []string {
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (r *ToolRegistry) GetDefinitions() []map[string]any {
	r.mu.RLock()
	defer r.mu.RUnlock()

	sorted := r.sortedToolNames()
	definitions := make([]map[string]any, 0, len(sorted))
	for _, name := range sorted {
		entry := r.tools[name]

		if !entry.IsCore && entry.TTL <= 0 {
			continue
		}

		definitions = append(definitions, ToolToSchema(r.tools[name].Tool))
	}
	return definitions
}

// ToProviderDefs converts tool definitions to provider-compatible format.
// This is the format expected by LLM provider APIs.
//
// MCP tools are advertised under their bare ExternalName ("<server>_<tool>") so a
// model sees a single naming convention across native, fusion, and upstream-MCP
// tools (matching what the MCP host publishes to CLI clients). Presenting the
// internal "mcp_" prefix here made it the lone prefixed outlier, so models routinely
// dropped it and the call was then rejected as unknown/not-permitted. resolve()
// maps the bare name back for dispatch and gating.
func (r *ToolRegistry) ToProviderDefs() []providers.ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()

	sorted := r.sortedToolNames()

	// Precompute collision info so a bare ExternalName is only used when it can't
	// shadow another tool: skip it if it equals some tool's internal name or if two
	// tools want the same bare name. Those fall back to the internal name.
	internalNames := make(map[string]bool, len(sorted))
	for _, name := range sorted {
		internalNames[name] = true
	}
	extWant := make(map[string]int)
	for _, name := range sorted {
		if en, ok := r.tools[name].Tool.(ExternalNamer); ok {
			if ext := en.ExternalName(); ext != name {
				extWant[ext]++
			}
		}
	}

	definitions := make([]providers.ToolDefinition, 0, len(sorted))
	for _, name := range sorted {
		entry := r.tools[name]

		if !entry.IsCore && entry.TTL <= 0 {
			continue
		}

		schema := ToolToSchema(entry.Tool)

		// Safely extract nested values with type checks
		fn, ok := schema["function"].(map[string]any)
		if !ok {
			continue
		}

		desc, _ := fn["description"].(string)
		params, _ := fn["parameters"].(map[string]any)

		pubName := name
		if en, ok := entry.Tool.(ExternalNamer); ok {
			if ext := en.ExternalName(); ext != name && !internalNames[ext] && extWant[ext] == 1 {
				pubName = ext
			}
		}

		definitions = append(definitions, providers.ToolDefinition{
			Type: "function",
			Function: providers.ToolFunctionDefinition{
				Name:        pubName,
				Description: desc,
				Parameters:  params,
			},
		})
	}
	return definitions
}

// IsPrimaryOnlyTool reports whether the named tool is restricted to primary
// agents (see PrimaryOnlyTool / IsPrimaryOnly). Unknown tools report false.
func (r *ToolRegistry) IsPrimaryOnlyTool(name string) bool {
	if entry, _, ok := r.resolve(name); ok {
		return IsPrimaryOnly(entry.Tool)
	}
	return false
}

// ToProviderDefsExcludingPrimaryOnly is ToProviderDefs with primary-only tools
// removed — the tool list offered to a spawned sub-agent's model.
func (r *ToolRegistry) ToProviderDefsExcludingPrimaryOnly() []providers.ToolDefinition {
	all := r.ToProviderDefs()
	out := make([]providers.ToolDefinition, 0, len(all))
	for _, d := range all {
		if r.IsPrimaryOnlyTool(d.Function.Name) {
			continue
		}
		out = append(out, d)
	}
	return out
}

// List returns a list of all registered tool names.
func (r *ToolRegistry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return r.sortedToolNames()
}

// Count returns the number of registered tools.
func (r *ToolRegistry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.tools)
}

// GetSummaries returns human-readable summaries of all registered tools.
// Returns a slice of "name - description" strings.
func (r *ToolRegistry) GetSummaries() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	sorted := r.sortedToolNames()
	summaries := make([]string, 0, len(sorted))
	for _, name := range sorted {
		entry := r.tools[name]

		if !entry.IsCore && entry.TTL <= 0 {
			continue
		}

		summaries = append(summaries, fmt.Sprintf("- `%s` - %s", entry.Tool.Name(), entry.Tool.Description()))
	}
	return summaries
}
