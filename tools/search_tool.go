package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/utils"
)

const (
	MaxRegexPatternLength = 200
)

// SearchTool (search_tools) is the entry point of progressive discovery: it
// returns the hidden tools matching a query, one line each (name and first
// line of description), and references them (ToolResult.ToolReferences), which
// loads their deferred definitions so the model can call them right away.
// get_tool_details shows one tool's full schema. BM25 natural-language search
// is the default; pass regex:true to match a regular expression over tool
// name/description instead. ToProviderDefs marks it as the tool-search tool.
type SearchTool struct {
	registry         *ToolRegistry
	maxSearchResults int

	// BM25 corpus cache, rebuilt only when the registry version changes.
	cacheMu      sync.Mutex
	cachedEngine *bm25CachedEngine
	cacheVersion uint64
}

func NewSearchTool(r *ToolRegistry, maxSearchResults int) *SearchTool {
	return &SearchTool{registry: r, maxSearchResults: maxSearchResults}
}

func (t *SearchTool) Name() string { return "search_tools" }

func (t *SearchTool) Description() string {
	// The namespace list changes only when the registered tools do, never
	// with what a conversation has searched, so the tool list stays stable.
	desc := "Search for available tools by capability. Returns matching tool names with one-line descriptions and makes those tools available: call one directly, or call get_tool_details on a name to see its full schema. Natural-language query by default; set regex:true to match a regular expression if the natural-language search can't find the tool."
	if ns := t.registry.HiddenNamespaces(); len(ns) > 0 {
		desc += " Discoverable tool groups: " + strings.Join(ns, ", ") + "."
	}
	return desc
}

func (t *SearchTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "What you want to do (natural language), or a regex pattern when regex is true.",
			},
			"regex": map[string]any{
				"type":        "boolean",
				"description": "Treat query as a regex over tool name/description instead of a natural-language search.",
			},
		},
		"required": []string{"query"},
	}
}

func (t *SearchTool) Execute(_ context.Context, args map[string]any) *ToolResult {
	query, ok := args["query"].(string)
	if !ok || strings.TrimSpace(query) == "" {
		// An empty query matches every hidden tool, dumping the whole catalog into
		// context and burning tokens.
		return ErrorResult("Missing or invalid 'query' argument. Must be a non-empty string.")
	}
	var useRegex bool
	if v, ok := args["regex"].(bool); ok {
		useRegex = v
	}

	var results []ToolSearchResult
	if useRegex {
		if len(query) > MaxRegexPatternLength {
			return ErrorResult(fmt.Sprintf("Pattern too long: max %d characters allowed", MaxRegexPatternLength))
		}
		var err error
		results, err = t.registry.SearchRegex(query, t.maxSearchResults)
		if err != nil {
			return ErrorResult(fmt.Sprintf("Invalid regex pattern syntax: %v. Please fix your regex and try again.", err))
		}
	} else if cached := t.getOrBuildEngine(); cached != nil {
		ranked := cached.engine.Search(query, t.maxSearchResults)
		results = make([]ToolSearchResult, 0, len(ranked))
		for _, r := range ranked {
			results = append(results, ToolSearchResult{Name: r.Document.Name, Description: r.Document.Description})
		}
	}

	if len(results) == 0 {
		return SilentResult("No tools found matching the query.")
	}

	logger.InfoCF("discovery", "Tool search completed",
		map[string]any{"query": query, "regex": useRegex, "results": len(results)})

	var b strings.Builder
	fmt.Fprintf(&b, "Found %d tool(s):\n", len(results))
	refs := make([]string, 0, len(results))
	for _, r := range results {
		fmt.Fprintf(&b, "- %s: %s\n", r.Name, firstLine(r.Description))
		refs = append(refs, r.Name)
	}
	b.WriteString("\nThese tools can be called now. Call get_tool_details(name) to see a tool's full schema.")
	res := SilentResult(b.String())
	res.ToolReferences = refs
	return res
}

// firstLine returns the first non-empty line of s, trimmed.
func firstLine(s string) string {
	for line := range strings.SplitSeq(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

// ToolDetailsTool (get_tool_details) returns the full schema of one tool and,
// for a hidden tool, references it (with the rest of its group when the group
// is reveal-together), which loads its deferred definition.
type ToolDetailsTool struct {
	registry *ToolRegistry
}

func NewToolDetailsTool(r *ToolRegistry) *ToolDetailsTool {
	return &ToolDetailsTool{registry: r}
}

func (t *ToolDetailsTool) Name() string { return "get_tool_details" }

func (t *ToolDetailsTool) Description() string {
	return "Show the full input schema of a tool found via search_tools. Pass the exact tool name from the search results."
}

func (t *ToolDetailsTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "Exact tool name from search_tools results.",
			},
		},
		"required": []string{"name"},
	}
}

func (t *ToolDetailsTool) Execute(_ context.Context, args map[string]any) *ToolResult {
	name, ok := args["name"].(string)
	if !ok || strings.TrimSpace(name) == "" {
		return ErrorResult("Missing or invalid 'name' argument. Pass an exact tool name from search_tools.")
	}
	name = strings.TrimSpace(name)

	schema, published, refs, ok := t.registry.toolDetails(name)
	if !ok {
		return ErrorResult(fmt.Sprintf("No tool named %q. Use search_tools to find the correct name.", name))
	}

	b, err := json.Marshal(schema)
	if err != nil {
		return ErrorResult("Failed to format tool schema: " + err.Error())
	}
	logger.InfoCF("discovery", "Tool details returned", map[string]any{"tool": published, "references": len(refs)})
	res := SilentResult(fmt.Sprintf("%s\n\nCall it as `%s`.", string(b), published))
	res.ToolReferences = refs
	return res
}

// ToolSearchResult represents a search hit returned to the LLM. Schemas are
// omitted to save context tokens: the hit is referenced, which loads its
// definition, and get_tool_details shows the schema as text on demand.
type ToolSearchResult struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// SearchRegex returns hidden tools whose model-facing name or description matches
// the (case-insensitive) pattern, up to maxSearchResults.
func (r *ToolRegistry) SearchRegex(pattern string, maxSearchResults int) ([]ToolSearchResult, error) {
	if maxSearchResults <= 0 {
		return nil, nil
	}

	regex, err := regexp.Compile("(?i)" + pattern)
	if err != nil {
		return nil, fmt.Errorf("failed to compile regex pattern %q: %w", pattern, err)
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	published := r.publishedNamesLocked()
	var results []ToolSearchResult
	// Iterate in sorted order for deterministic results across calls.
	for _, name := range r.sortedToolNames() {
		entry := r.tools[name]
		if entry.IsCore {
			continue // core tools are already loaded
		}
		adv := published[name]
		desc := entry.Tool.Description()
		if regex.MatchString(adv) || regex.MatchString(desc) {
			results = append(results, ToolSearchResult{Name: adv, Description: desc})
			if len(results) >= maxSearchResults {
				break
			}
		}
	}
	return results, nil
}

// Lightweight internal type used as corpus document for BM25.
type searchDoc struct {
	Name        string
	Description string
}

// bm25CachedEngine wraps a BM25Engine with its corpus snapshot.
type bm25CachedEngine struct {
	engine *utils.BM25Engine[searchDoc]
}

func snapshotToSearchDocs(snap HiddenToolSnapshot) []searchDoc {
	docs := make([]searchDoc, len(snap.Docs))
	for i, d := range snap.Docs {
		docs[i] = searchDoc(d)
	}
	return docs
}

func buildBM25Engine(docs []searchDoc) *utils.BM25Engine[searchDoc] {
	return utils.NewBM25Engine(
		docs,
		func(doc searchDoc) string {
			return doc.Name + " " + doc.Description
		},
	)
}

// getOrBuildEngine returns a cached BM25 engine, rebuilding it only when the
// registry version has changed (new tools registered).
func (t *SearchTool) getOrBuildEngine() *bm25CachedEngine {
	if t.cachedEngine != nil && t.cacheVersion == t.registry.Version() {
		return t.cachedEngine
	}

	t.cacheMu.Lock()
	defer t.cacheMu.Unlock()

	// Snapshot + version are read under a single registry RLock (no TOCTOU).
	snap := t.registry.SnapshotHiddenTools()

	if t.cachedEngine != nil && t.cacheVersion == snap.Version {
		return t.cachedEngine
	}

	docs := snapshotToSearchDocs(snap)
	if len(docs) == 0 {
		t.cachedEngine = nil
		t.cacheVersion = snap.Version
		return nil
	}

	cached := &bm25CachedEngine{engine: buildBM25Engine(docs)}
	t.cachedEngine = cached
	t.cacheVersion = snap.Version
	logger.DebugCF("discovery", "BM25 engine rebuilt", map[string]any{"docs": len(docs), "version": snap.Version})
	return cached
}

// SearchBM25 ranks hidden tools against query using BM25. This non-cached variant
// rebuilds the engine on every call; used by tests and callers without a SearchTool.
func (r *ToolRegistry) SearchBM25(query string, maxSearchResults int) []ToolSearchResult {
	snap := r.SnapshotHiddenTools()
	docs := snapshotToSearchDocs(snap)
	if len(docs) == 0 {
		return nil
	}

	ranked := buildBM25Engine(docs).Search(query, maxSearchResults)
	if len(ranked) == 0 {
		return nil
	}

	out := make([]ToolSearchResult, len(ranked))
	for i, r := range ranked {
		out[i] = ToolSearchResult{Name: r.Document.Name, Description: r.Document.Description}
	}
	return out
}
