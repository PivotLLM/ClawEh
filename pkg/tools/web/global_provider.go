package web

import (
	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/global"
	"github.com/PivotLLM/ClawEh/pkg/tools"
)

// GlobalProvider exposes the web tools through the transport-neutral global
// layer with BARE names ("search", "fetch"). The aggregator mounts it under the
// "web" namespace, so the published names are "web_search" / "web_fetch". It
// reuses the existing WebSearchTool / WebFetchTool logic and converts the result
// at the boundary, so behaviour is unchanged.
var GlobalProvider globalWebProvider

type globalWebProvider struct{}

// Namespace/Description/Available satisfy global.HostMeta.
func (globalWebProvider) Namespace() string   { return "web" }
func (globalWebProvider) Description() string { return "Web search and fetch operations" }

func (globalWebProvider) Available(cfg any) (bool, string) {
	return true, ""
}

func (globalWebProvider) RegisterTools(deps global.Deps) []global.ToolDefinition {
	// Construct the real tool instances only when real config is present.
	// Enumeration (Describe) passes a zero Deps; handlers are never called then,
	// so leaving the instances nil is safe. Static metadata below is derived from
	// zero-value instances, whose Description()/Parameters() return static literals.
	var search *WebSearchTool
	var fetch *WebFetchTool

	c, _ := deps.Cfg.(*config.Config)
	_, _ = deps.Host.(tools.ToolDeps)

	if c != nil {
		if s, err := NewWebSearchTool(WebSearchToolOptions{
			BraveAPIKeys:         config.MergeAPIKeys(c.Tools.Web.Brave.APIKey, c.Tools.Web.Brave.APIKeys),
			BraveMaxResults:      c.Tools.Web.Brave.MaxResults,
			BraveEnabled:         c.Tools.Web.Brave.Enabled,
			TavilyAPIKeys:        config.MergeAPIKeys(c.Tools.Web.Tavily.APIKey, c.Tools.Web.Tavily.APIKeys),
			TavilyBaseURL:        c.Tools.Web.Tavily.BaseURL,
			TavilyMaxResults:     c.Tools.Web.Tavily.MaxResults,
			TavilyEnabled:        c.Tools.Web.Tavily.Enabled,
			DuckDuckGoMaxResults: c.Tools.Web.DuckDuckGo.MaxResults,
			DuckDuckGoEnabled:    c.Tools.Web.DuckDuckGo.Enabled,
			PerplexityAPIKeys: config.MergeAPIKeys(
				c.Tools.Web.Perplexity.APIKey,
				c.Tools.Web.Perplexity.APIKeys,
			),
			PerplexityMaxResults: c.Tools.Web.Perplexity.MaxResults,
			PerplexityEnabled:    c.Tools.Web.Perplexity.Enabled,
			SearXNGBaseURL:       c.Tools.Web.SearXNG.BaseURL,
			SearXNGMaxResults:    c.Tools.Web.SearXNG.MaxResults,
			SearXNGEnabled:       c.Tools.Web.SearXNG.Enabled,
			GLMSearchAPIKey:      c.Tools.Web.GLMSearch.APIKey,
			GLMSearchBaseURL:     c.Tools.Web.GLMSearch.BaseURL,
			GLMSearchEngine:      c.Tools.Web.GLMSearch.SearchEngine,
			GLMSearchMaxResults:  c.Tools.Web.GLMSearch.MaxResults,
			GLMSearchEnabled:     c.Tools.Web.GLMSearch.Enabled,
			Proxy:                c.Tools.Web.Proxy,
		}); err == nil {
			search = s
		}
		if f, err := NewWebFetchToolWithProxy(50000, c.Tools.Web.Proxy, c.Tools.Web.FetchLimitBytes); err == nil {
			fetch = f
		}
	}

	return []global.ToolDefinition{
		{
			Name:         "search",
			Description:  (&WebSearchTool{}).Description(),
			RawSchema:    (&WebSearchTool{}).Parameters(),
			Category:     "web",
			DefaultAllow: global.Allow(true),
			Handler: func(call *global.ToolCall) (*global.Result, error) {
				return tools.ResultToGlobal(search.Execute(call.Ctx, call.Args)), nil
			},
		},
		{
			Name:         "fetch",
			Description:  (&WebFetchTool{}).Description(),
			RawSchema:    (&WebFetchTool{}).Parameters(),
			Category:     "web",
			DefaultAllow: global.Allow(true),
			Handler: func(call *global.ToolCall) (*global.Result, error) {
				return tools.ResultToGlobal(fetch.Execute(call.Ctx, call.Args)), nil
			},
		},
	}
}
