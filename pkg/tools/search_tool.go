package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/PivotLLM/ClawEh/pkg/logger"
	"github.com/PivotLLM/ClawEh/pkg/utils"
)

const (
	MaxRegexPatternLength = 200
)

// SearchTool (search_tools) is layer 1 of progressive discovery: it returns the
// names and one-line descriptions of hidden tools matching a query, WITHOUT loading
// their schemas or unlocking them. The model then calls get_tool_details on a chosen
// name (layer 2) to load its schema and promote it, then calls it (layer 3). BM25
// natural-language search is the default; pass regex:true to match a regular
// expression over tool name/description instead.
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
	desc := "Search for available tools by capability. Returns matching tool names and one-line descriptions only — call get_tool_details on a result's name to load its full schema and unlock it before use. Natural-language query by default; set regex:true to match a regular expression if the natural-language search can't find the tool."
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
	useRegex, _ := args["regex"].(bool)

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

	b, err := json.Marshal(results)
	if err != nil {
		return ErrorResult("Failed to format search results: " + err.Error())
	}
	return SilentResult(fmt.Sprintf(
		"Found %d tool(s):\n%s\n\nCall get_tool_details(name) on the one you need to load its schema and unlock it, then call it.",
		len(results), string(b)))
}

// ToolDetailsTool (get_tool_details) is layer 2 of progressive discovery: it loads
// the full schema for a single tool discovered via search_tools and promotes it so
// it becomes callable for the next TTL turns.
type ToolDetailsTool struct {
	registry      *ToolRegistry
	ttl           int
	visibleBudget int
}

func NewToolDetailsTool(r *ToolRegistry, ttl, visibleBudget int) *ToolDetailsTool {
	return &ToolDetailsTool{registry: r, ttl: ttl, visibleBudget: visibleBudget}
}

func (t *ToolDetailsTool) Name() string { return "get_tool_details" }

func (t *ToolDetailsTool) Description() string {
	return "Load the full input schema for a tool found via search_tools and unlock it so you can call it. Pass the exact tool name from the search results."
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

	schema, advertised, _, ok := t.registry.revealTool(name, t.ttl, t.visibleBudget)
	if !ok {
		return ErrorResult(fmt.Sprintf("No tool named %q. Use search_tools to find the correct name.", name))
	}

	b, err := json.Marshal(schema)
	if err != nil {
		return ErrorResult("Failed to format tool schema: " + err.Error())
	}
	logger.InfoCF("discovery", "Tool details revealed", map[string]any{"tool": advertised, "ttl": t.ttl})
	return SilentResult(fmt.Sprintf(
		"%s\n\nThis tool is now unlocked. Call it as `%s` in your next response.",
		string(b), advertised))
}

// ToolSearchResult represents a search hit returned to the LLM. Schemas are omitted
// to save context tokens; the LLM loads a schema via get_tool_details on demand.
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

	var results []ToolSearchResult
	// Iterate in sorted order for deterministic results across calls.
	for _, name := range r.sortedToolNames() {
		entry := r.tools[name]
		if entry.IsCore {
			continue // core tools are already visible
		}
		adv := advertisedName(entry.Tool, name)
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
		docs[i] = searchDoc{Name: d.Name, Description: d.Description}
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
