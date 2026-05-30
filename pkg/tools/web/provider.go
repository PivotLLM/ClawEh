package web

import (
	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/logger"
	"github.com/PivotLLM/ClawEh/pkg/tools"
)

// Provider is the singleton ToolProvider for web tools.
var Provider webProvider

type webProvider struct{}

func (p webProvider) Namespace() string   { return "web" }
func (p webProvider) Description() string { return "Web search and fetch operations" }
func (p webProvider) Category() string    { return "web" }
func (p webProvider) ConfigKey() string   { return "web" }

func (p webProvider) Available(cfg *config.Config) (bool, string) {
	return true, ""
}

func (p webProvider) Build(deps tools.ToolDeps) []tools.Tool {
	cfg := deps.Cfg
	if cfg == nil {
		return nil
	}
	agentCfg := deps.AgentCfg
	agentID := deps.AgentID

	var result []tools.Tool

	if cfg.Tools.IsToolEnabled("web") && isToolAllowed(agentCfg, "web_search") {
		searchTool, err := NewWebSearchTool(WebSearchToolOptions{
			BraveAPIKeys:         config.MergeAPIKeys(cfg.Tools.Web.Brave.APIKey, cfg.Tools.Web.Brave.APIKeys),
			BraveMaxResults:      cfg.Tools.Web.Brave.MaxResults,
			BraveEnabled:         cfg.Tools.Web.Brave.Enabled,
			TavilyAPIKeys:        config.MergeAPIKeys(cfg.Tools.Web.Tavily.APIKey, cfg.Tools.Web.Tavily.APIKeys),
			TavilyBaseURL:        cfg.Tools.Web.Tavily.BaseURL,
			TavilyMaxResults:     cfg.Tools.Web.Tavily.MaxResults,
			TavilyEnabled:        cfg.Tools.Web.Tavily.Enabled,
			DuckDuckGoMaxResults: cfg.Tools.Web.DuckDuckGo.MaxResults,
			DuckDuckGoEnabled:    cfg.Tools.Web.DuckDuckGo.Enabled,
			PerplexityAPIKeys: config.MergeAPIKeys(
				cfg.Tools.Web.Perplexity.APIKey,
				cfg.Tools.Web.Perplexity.APIKeys,
			),
			PerplexityMaxResults: cfg.Tools.Web.Perplexity.MaxResults,
			PerplexityEnabled:    cfg.Tools.Web.Perplexity.Enabled,
			SearXNGBaseURL:       cfg.Tools.Web.SearXNG.BaseURL,
			SearXNGMaxResults:    cfg.Tools.Web.SearXNG.MaxResults,
			SearXNGEnabled:       cfg.Tools.Web.SearXNG.Enabled,
			GLMSearchAPIKey:      cfg.Tools.Web.GLMSearch.APIKey,
			GLMSearchBaseURL:     cfg.Tools.Web.GLMSearch.BaseURL,
			GLMSearchEngine:      cfg.Tools.Web.GLMSearch.SearchEngine,
			GLMSearchMaxResults:  cfg.Tools.Web.GLMSearch.MaxResults,
			GLMSearchEnabled:     cfg.Tools.Web.GLMSearch.Enabled,
			Proxy:                cfg.Tools.Web.Proxy,
		})
		if err != nil {
			logger.ErrorCF("agent", "Failed to create web search tool", map[string]any{"agent_id": agentID, "error": err.Error()})
		} else if searchTool != nil {
			result = append(result, searchTool)
		}
	}

	if cfg.Tools.IsToolEnabled("web_fetch") && isToolAllowed(agentCfg, "web_fetch") {
		fetchTool, err := NewWebFetchToolWithProxy(50000, cfg.Tools.Web.Proxy, cfg.Tools.Web.FetchLimitBytes)
		if err != nil {
			logger.ErrorCF("agent", "Failed to create web fetch tool", map[string]any{"agent_id": agentID, "error": err.Error()})
		} else {
			result = append(result, fetchTool)
		}
	}

	return result
}

func (p webProvider) Describe() []tools.ToolDescriptor {
	return []tools.ToolDescriptor{
		{Name: "web_search", Description: "Search the web using the configured providers.", Category: "web", ConfigKey: "web", DefaultEnabled: true},
		{Name: "web_fetch", Description: "Fetch and summarize the contents of a webpage.", Category: "web", ConfigKey: "web_fetch", DefaultEnabled: true},
	}
}

// isToolAllowed checks whether the agent config permits the named tool.
func isToolAllowed(agentCfg *config.AgentConfig, name string) bool {
	if agentCfg == nil {
		return true
	}
	return agentCfg.IsToolAllowed(name)
}
