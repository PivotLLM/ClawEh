# Web search and fetch

Agents search the web with `web_search` and read pages with `web_fetch`; this page shows how to set up a search provider and grant the tools.

## What an agent needs

1. **A search provider.** None is set up on a new install, so `web_search` answers "web search is not configured on this server (no search provider is enabled)" until you enable one. `web_fetch` needs no setup.
2. **The tools granted.** Tick `web_search` and `web_fetch` under **Internal tools** on the agent's card on the **Agents** page. **Default tools** includes both. See [Tools and skills](./tools-and-skills.md).
3. **The tools enabled install-wide.** On the **Tools** page, under **Web**, both are **Enabled** unless you disabled them.

## Search providers

ClawEh supports six search services. Only one is used at a time: the first in this list that is enabled and has what it needs.

| Order | Provider | Section in `config.json` | Needs |
|---|---|---|---|
| 1 | Perplexity | `tools.web.perplexity` | `api_key` |
| 2 | Brave Search | `tools.web.brave` | `api_key` |
| 3 | SearXNG | `tools.web.searxng` | `base_url` of your SearXNG server |
| 4 | Tavily | `tools.web.tavily` | `api_key` |
| 5 | DuckDuckGo | `tools.web.duckduckgo` | nothing |
| 6 | GLM Search (Zhipu) | `tools.web.glm_search` | `api_key` |

A provider counts only when its `enabled` is `true` **and** its key or address is filled in. An enabled provider with an empty key is skipped.

There is no fallback between providers. If the chosen provider fails, the search fails and the agent is told why; the next provider is not tried. To switch providers, disable the first one.

### Settings for each provider

Every provider has `enabled` and `max_results` (results per search when the agent does not ask for a number; default 5). The agent can ask for 1 to 10 results.

| Provider | Other settings |
|---|---|
| Perplexity | `api_key`, `api_keys`. Uses Perplexity's `sonar` model, so a search is slower than the others. |
| Brave Search | `api_key`, `api_keys`. |
| SearXNG | `base_url`, for example `http://searx.example.com`. |
| Tavily | `api_key`, `api_keys`, `base_url` (leave empty for Tavily's own service). |
| DuckDuckGo | None. It reads DuckDuckGo's HTML results page. |
| GLM Search | `api_key`, `base_url` (default `https://open.bigmodel.cn/api/paas/v4/web_search`), `search_engine`: `search_std` (default), `search_pro`, `search_pro_sogou` or `search_pro_quark`. |

**Several keys.** Perplexity, Brave and Tavily accept a list in `api_keys` as well as `api_key`. Searches start on the keys in turn, and when a key is refused, rate-limited or the service errors, the next key is tried.

## Set up a provider

There are no web search fields in the WebUI forms. Edit `config.json`, or open **Raw config** on the **System** page. Saving reloads the configuration; no restart is needed.

Example: Brave Search, with the key kept out of the file:

```json
{
  "tools": {
    "web": {
      "brave": {
        "enabled": true,
        "api_key": "env:BRAVE_API_KEY",
        "max_results": 5
      }
    }
  }
}
```

Example: your own SearXNG server:

```json
{
  "tools": {
    "web": {
      "searxng": {
        "enabled": true,
        "base_url": "http://127.0.0.1:8888"
      }
    }
  }
}
```

Keys may be written as `env:NAME` or `file:/path` references, in `api_key` and in each entry of `api_keys`. See [Keeping secrets out of the file](../reference/configuration.md#keeping-secrets-out-of-the-file). Each setting can also be set with an environment variable, for example `CLAW_TOOLS_WEB_BRAVE_API_KEY` or `CLAW_TOOLS_WEB_SEARXNG_BASE_URL`.

The full report on the [Check Up](../admin/checkup.md) page lists each provider under **Web tools**, with whether it is on and whether its key is set.

## Fetching pages

`web_fetch` downloads one `http` or `https` address and gives the agent its text.

- HTML is turned into plain text; JSON is passed on formatted; anything else as is.
- The agent gets up to 50,000 characters by default, and can ask for more.
- Downloads stop at 10 MB. `tools.web.fetch_limit_bytes` changes the limit.
- It gives up after 60 seconds or 5 redirects.
- Addresses on this machine or the local network (`localhost`, private and link-local addresses) are refused, including after a redirect.
- A page that yields almost no text, usually one built by JavaScript or behind a bot check, comes with a note suggesting a browser tool instead. A browser is available through an [MCP server](../mcp.md), such as Playwright.

Search results and fetched pages are marked to the model as content from the web, not as instructions.

## Through a proxy

`tools.web.proxy` sends searches and fetches through a proxy: `http://`, `https://`, `socks5://` or `socks5h://`. For a proxy that needs a password, set the `HTTP_PROXY`/`HTTPS_PROXY` environment variables of the service instead of writing the password into `config.json`.

SearXNG searches ignore `tools.web.proxy`.
