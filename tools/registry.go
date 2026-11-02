package tools

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/providers"
	"github.com/PivotLLM/ClawEh/utils"
)

type ToolEntry struct {
	Tool Tool
	// IsCore is false for a tool hidden by progressive discovery: it is still
	// offered to the model on every call, but deferred (loaded only once a
	// search_tools or get_tool_details result references it), and it may be
	// called at any time.
	IsCore bool
	// SuiteExempt marks a tool that belongs to an all-or-nothing suite (cogmem,
	// maestro). Suites are gated as a unit by the per-agent suite flag at
	// registration time, so their tools must also bypass the execution-time
	// per-tool allowlist check (which has no entry for them).
	SuiteExempt bool
	// Group names the discovery set this tool belongs to (MCP server or fusion
	// service). RevealTogether, when set, makes get_tool_details on one of the
	// Group's hidden tools reference every hidden tool of the Group, so a small
	// cohesive set loads in one call. Both empty/false for ungrouped tools.
	Group          string
	RevealTogether bool
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
		SuiteExempt: true,
	}
	r.version.Add(1)
	logger.DebugCF("tools", "Registered suite tool", map[string]any{"name": name})
}

// RegisterSuiteHidden registers a suite tool that is ALSO hidden behind
// progressive discovery: suite-exempt from the per-tool allowlist (the suite flag
// is the allow decision) yet hidden like any discovery tool, so it is offered
// deferred until a discovery result references it. Used for the fusion
// and maestro suites when an agent has progressive discovery on.
func (r *ToolRegistry) RegisterSuiteHidden(tool Tool) {
	r.RegisterSuiteHiddenGroup(tool, "", false)
}

// RegisterSuiteHiddenGroup is RegisterSuiteHidden with a discovery group and
// reveal-together flag, so a suite's tools can be loaded as a set.
func (r *ToolRegistry) RegisterSuiteHiddenGroup(tool Tool, group string, revealTogether bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	name := tool.Name()
	if _, exists := r.tools[name]; exists {
		logger.WarnCF("tools", "Tool registration overwrites existing tool",
			map[string]any{"name": name})
	}
	r.tools[name] = &ToolEntry{
		Tool:           tool,
		IsCore:         false, // deferred until referenced
		SuiteExempt:    true,  // gated by the suite flag, not the per-tool allowlist
		Group:          group,
		RevealTogether: revealTogether,
	}
	r.version.Add(1)
	logger.DebugCF("tools", "Registered hidden suite tool", map[string]any{"name": name, "group": group})
}

// DiscoveryGrouped is implemented by tools that belong to a reveal-together
// discovery group (an MCP server or a fusion service). Registration reads it to
// tag the hidden entry so get_tool_details on one member loads the whole group.
type DiscoveryGrouped interface {
	DiscoveryGroup() (group string, revealTogether bool)
}

// RegisterHidden saves a hidden tool: offered to the model deferred, loaded
// once a discovery result references it.
func (r *ToolRegistry) RegisterHidden(tool Tool) {
	r.RegisterHiddenGroup(tool, "", false)
}

// RegisterHiddenGroup is RegisterHidden with a discovery group and reveal-together
// flag, so an upstream server's (or service's) tools can be loaded as a set.
func (r *ToolRegistry) RegisterHiddenGroup(tool Tool, group string, revealTogether bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	name := tool.Name()
	if _, exists := r.tools[name]; exists {
		logger.WarnCF("tools", "Hidden tool registration overwrites existing tool",
			map[string]any{"name": name})
	}
	r.tools[name] = &ToolEntry{
		Tool:           tool,
		IsCore:         false,
		Group:          group,
		RevealTogether: revealTogether,
	}
	r.version.Add(1)
	logger.DebugCF("tools", "Registered hidden tool", map[string]any{"name": name, "group": group})
}

// RemoveByPrefix removes every registered tool, visible or hidden, whose name
// starts with prefix, and returns how many were removed. The version is bumped
// when anything was removed so cached definitions and search indexes rebuild.
// Used to drop an upstream MCP server's tools ("mcp_<server>_") as a set before
// re-registering its current list, so a renamed or removed tool disappears.
func (r *ToolRegistry) RemoveByPrefix(prefix string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	removed := 0
	for name := range r.tools {
		if strings.HasPrefix(name, prefix) {
			delete(r.tools, name)
			removed++
		}
	}
	if removed > 0 {
		r.version.Add(1)
		logger.DebugCF("tools", "Removed tools by prefix", map[string]any{"prefix": prefix, "count": removed})
	}
	return removed
}

// ReplaceByPrefix swaps every registered tool whose name starts with prefix for
// entries, in one step: a concurrent reader (a turn listing its tools, a tool
// call) sees the old set or the new one, never neither. An entry whose name
// lacks the prefix is registered all the same; the caller keeps them in step.
// Returns how many tools were removed. The version is bumped when anything
// was removed or added.
func (r *ToolRegistry) ReplaceByPrefix(prefix string, entries []ToolEntry) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	removed := 0
	for name := range r.tools {
		if strings.HasPrefix(name, prefix) {
			delete(r.tools, name)
			removed++
		}
	}
	for i := range entries {
		entry := entries[i]
		r.tools[entry.Tool.Name()] = &entry
	}
	if removed > 0 || len(entries) > 0 {
		r.version.Add(1)
	}
	logger.DebugCF("tools", "Replaced tools by prefix",
		map[string]any{"prefix": prefix, "removed": removed, "added": len(entries)})
	return removed
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
	published := r.publishedNamesLocked()
	docs := make([]HiddenToolDoc, 0, len(r.tools))
	for name, entry := range r.tools {
		if !entry.IsCore {
			docs = append(docs, HiddenToolDoc{
				// The name ToProviderDefs offers the tool under, so a search hit
				// can be referenced and called as is.
				Name:        published[name],
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

// Get returns the tool registered under name (the internal registry key),
// hidden or not: progressive discovery only defers loading a tool's
// definition, it never makes a registered tool uncallable.
func (r *ToolRegistry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.tools[name]
	if !ok {
		return nil, false
	}
	return entry.Tool, true
}

// resolve looks name up by internal key or ExternalName, once. entry is nil
// when no such tool is registered.
func (r *ToolRegistry) resolve(name string) (entry *ToolEntry, canonical string) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.findAnyLocked(name)
}

// advertisedName is a tool's bare ExternalName for MCP tools, else the
// internal registry name. ToProviderDefs uses it unless it collides
// (publishedNamesLocked).
func advertisedName(t Tool, internal string) string {
	if en, ok := t.(ExternalNamer); ok {
		if ext := en.ExternalName(); ext != "" {
			return ext
		}
	}
	return internal
}

// findAnyLocked returns the entry and internal name for a tool by internal key or
// ExternalName, hidden or not. Caller holds at least r.mu.RLock.
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

// publishedNamesLocked maps every internal tool name to the name ToProviderDefs
// offers it under: the bare ExternalName for an MCP tool, unless that would
// shadow another tool (it equals some tool's internal name, or two tools want
// the same bare name), else the internal name. Search hits and tool
// references use these names, so they match the tool list exactly. Caller
// holds at least r.mu.RLock.
func (r *ToolRegistry) publishedNamesLocked() map[string]string {
	extWant := make(map[string]int)
	for name, entry := range r.tools {
		if en, ok := entry.Tool.(ExternalNamer); ok {
			if ext := en.ExternalName(); ext != name {
				extWant[ext]++
			}
		}
	}
	out := make(map[string]string, len(r.tools))
	for name, entry := range r.tools {
		out[name] = name
		if en, ok := entry.Tool.(ExternalNamer); ok {
			if ext := en.ExternalName(); ext != "" && ext != name && r.tools[ext] == nil && extWant[ext] == 1 {
				out[name] = ext
			}
		}
	}
	return out
}

// toolDetails returns a tool's provider schema, the name it is offered under
// and the tool references a get_tool_details result carries for it. name may
// be the internal registry key or a bare ExternalName. A hidden tool is
// referenced, together with every other hidden tool of its group when the
// group is reveal-together (sorted after it); a core tool, already loaded,
// is not. ok is false when there is no such tool.
func (r *ToolRegistry) toolDetails(name string) (schema map[string]any, published string, refs []string, ok bool) {
	r.mu.RLock()
	entry, in := r.findAnyLocked(name)
	if entry == nil {
		r.mu.RUnlock()
		return nil, "", nil, false
	}
	names := r.publishedNamesLocked()
	published = names[in]
	if !entry.IsCore {
		refs = append(refs, published)
		if entry.RevealTogether && entry.Group != "" {
			var group []string
			for sibName, sib := range r.tools {
				if sibName != in && !sib.IsCore && sib.Group == entry.Group {
					group = append(group, names[sibName])
				}
			}
			sort.Strings(group)
			refs = append(refs, group...)
		}
	}
	tool := entry.Tool
	r.mu.RUnlock()
	// Outside the lock: a tool's Description may read the registry itself.
	return ToolToSchema(tool), published, refs, true
}

func (r *ToolRegistry) Execute(ctx context.Context, name string, args map[string]any) *ToolResult {
	return r.ExecuteWithContext(ctx, name, args, "", "", nil)
}

// ExecuteWithContext executes a registered tool, hidden by discovery or not,
// with channel/chatID context and optional async callback.
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
	entry, canonical := r.resolve(name)
	if entry == nil {
		logger.ErrorCF("tool", "Tool not found",
			map[string]any{
				"tool": name,
			})
		return ErrorResult(fmt.Sprintf("tool %q not found", name)).WithError(errors.New("tool not found"))
	}

	// Defense-in-depth: check tool allowlist from context before execution.
	// Suite tools (cogmem, maestro, fusion) are exempt from the ALLOW side — they
	// are gated as a unit by the per-agent suite flag at registration, not by the
	// per-tool allowlist — but the agent's deny list still applies to them. Gate
	// on the canonical (internal) name so MCP tools route to the mcp_tools
	// allow-list even when the caller used the bare ExternalName.
	if checker := ToolAllowCheckerFromCtx(ctx); checker != nil {
		if checker.IsToolDenied(canonical) || (!entry.SuiteExempt && !checker.IsToolAllowed(canonical)) {
			logger.WarnCF("tool", "Tool execution denied by agent allowlist",
				map[string]any{
					"tool": canonical,
				})
			return ErrorResult(NotEnabledMessage(name)).WithError(Refusal(fmt.Errorf("tool not permitted: %s", name)))
		}
	}

	tool := entry.Tool

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
	if result.IsError && IsExpectedRefusal(result.Err) {
		// A configured permission or limit said no: expected, not a fault.
		logger.WarnCF("tool", "Tool call refused",
			map[string]any{
				"tool":     name,
				"duration": duration.Milliseconds(),
				"error":    utils.Truncate(result.ForLLM, 500),
			})
	} else if result.IsError {
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

// GetDefinitions returns the schemas of the core tools (those not hidden by
// discovery).
func (r *ToolRegistry) GetDefinitions() []map[string]any {
	core := r.coreTools()
	definitions := make([]map[string]any, 0, len(core))
	for _, tool := range core {
		definitions = append(definitions, ToolToSchema(tool))
	}
	return definitions
}

// coreTools returns the core tools sorted by name. They are collected under
// the lock and returned so a caller builds schemas or descriptions after it:
// a tool's Description may read the registry itself (search_tools).
func (r *ToolRegistry) coreTools() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	sorted := r.sortedToolNames()
	out := make([]Tool, 0, len(sorted))
	for _, name := range sorted {
		if entry := r.tools[name]; entry.IsCore {
			out = append(out, entry.Tool)
		}
	}
	return out
}

// ToProviderDefs converts tool definitions to provider-compatible format.
// This is the format expected by LLM provider APIs.
//
// Every registered tool is included, sorted by internal name, so the list is
// the same on every call while the registered set is unchanged (a stable
// prompt-cache prefix). A tool hidden by progressive discovery is marked
// Deferred: spawnllm loads it once a tool result references it
// (ToolResult.ToolReferences), natively where the provider can and otherwise
// by sending it as an ordinary tool from then on. search_tools is marked
// ToolSearch. With discovery off every tool is core and neither flag is set.
//
// MCP tools are advertised under their bare ExternalName ("<server>_<tool>") so a
// model sees a single naming convention across native, fusion, and upstream-MCP
// tools (matching what the MCP host publishes to CLI clients). Presenting the
// internal "mcp_" prefix here made it the lone prefixed outlier, so models routinely
// dropped it and the call was then rejected as unknown/not-permitted. resolve()
// maps the bare name back for dispatch and gating.
func (r *ToolRegistry) ToProviderDefs() []providers.ToolDefinition {
	type offered struct {
		entry     *ToolEntry
		published string
	}
	// Entries are never mutated after registration, so they are read under
	// the lock and their schemas built after it: a tool's Description may
	// read the registry itself (search_tools lists the hidden namespaces).
	r.mu.RLock()
	sorted := r.sortedToolNames()
	names := r.publishedNamesLocked()
	list := make([]offered, 0, len(sorted))
	for _, name := range sorted {
		list = append(list, offered{entry: r.tools[name], published: names[name]})
	}
	r.mu.RUnlock()

	definitions := make([]providers.ToolDefinition, 0, len(list))
	for _, o := range list {
		entry := o.entry
		schema := ToolToSchema(entry.Tool)

		// Safely extract nested values with type checks
		fn, ok := schema["function"].(map[string]any)
		if !ok {
			continue
		}

		var desc string
		if v, ok := fn["description"].(string); ok {
			desc = v
		}
		var params map[string]any
		if v, ok := fn["parameters"].(map[string]any); ok {
			params = v
		}

		_, isSearch := entry.Tool.(*SearchTool)
		definitions = append(definitions, providers.ToolDefinition{
			Type: "function",
			Function: providers.ToolFunctionDefinition{
				Name:        o.published,
				Description: desc,
				Parameters:  params,
			},
			Deferred:   !entry.IsCore,
			ToolSearch: isSearch,
		})
	}
	return definitions
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

// GetSummaries returns human-readable summaries of the core tools (those not
// hidden by discovery). Returns a slice of "name - description" strings.
func (r *ToolRegistry) GetSummaries() []string {
	core := r.coreTools()
	summaries := make([]string, 0, len(core))
	for _, tool := range core {
		summaries = append(summaries, fmt.Sprintf("- `%s` - %s", tool.Name(), tool.Description()))
	}
	return summaries
}
