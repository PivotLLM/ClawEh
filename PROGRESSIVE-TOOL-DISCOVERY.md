# Progressive Tool Discovery — Implementation Plan

Status: **backend + MCP host + frontend implemented & tested** (Go pkgs green; tsc clean; SPA builds).
Global switch (default off); MCP host does per-session progressive discovery. Only the optional
doctor migration for stale `tools.mcp.discovery` remains (non-urgent; lenient loader ignores it).

Frontend (done):
- MCP page: "Progressive Tool Discovery" card — global `tools.discovery.enabled` switch +
  `always_shown_namespaces` list editor (search/get_tool_details/cogmem always shown by rule).
- Agent editor: "Tools" → "Always-On Tools" with a native-tools-only note; removed the greyed
  cogmem/maestro/fusion suite rows (`tool-select.tsx`).
- `file_*_bytes` default-off (backend) reflected in the catalog automatically.

MCP host (done):
- Global `tools.discovery.enabled` (default off), applied to native loop AND host.
- `mcp_host.always_shown_namespaces` (default ["cogmem"]); search_tools/get_tool_details always shown.
- Host tools/list omits progressive tools (`addToolsToServer` + `discoveryConfig.baseVisible`).
- `get_tool_details` over the host reveals per-session via `AddSessionTool` + `list_changed`
  (`revealForSession`), so a client only ever sees/uses its own agent's tools — no union leak.
- Search runs in the authenticated agent's registry (per-agent filtered by construction).

Done:
- Config: `AgentConfig.progressive_discovery *bool` (nil=on); `tools.discovery.auto_threshold *int` (nil=120) moved to `tools.discovery`; removed global `enabled`/`use_bm25`/`use_regex`.
- Registry: `RegisterSuiteHidden` (hidden + suite-exempt); TTL-reset-on-use; `revealTool`; hidden listings use advertised (bare) names.
- Meta-tools: `search_tools` (bm25 default, `regex:true` opt-in, names only) + `get_tool_details` (schema + promote); retired `find_tools_*`.
- Loop: build→count→decide effective discovery (pref OR count>threshold), store on `AgentInstance.DiscoveryActive`; native+cogmem always-on, fusion/maestro→`RegisterSuiteHidden`, MCP→`RegisterHidden`; meta-tools registered only when active; context-prompt rule updated.
- Defaults: `file_*_bytes` tools default-off; backend catalog no longer lists find_tools.

Remaining:
- Frontend: rename "Tools" → always-on framing; remove greyed suite rows; per-agent toggle; Service-card `auto_threshold` field.
- Doctor migration to drop stale `tools.mcp.discovery` (non-urgent: lenient loader ignores it; discovery defaults on).

## Goal

Stop advertising every tool to the model on every request. Keep a small, curated
**always-on** set in context and make everything else **discoverable** on demand via
a 3-layer search flow. Per-agent switch, default on, with a global tool-count
safety threshold that force-enables it. This fixes the provider tool-cap failures
(GPT 128, X.ai/grok 200) that currently degrade to a silent empty reply or a
mislabeled 400 when an agent advertises ~330 tools.

## Model

Two orthogonal properties decide whether a tool is in context:

1. **Per-agent switch** — `progressive_discovery` (default **on**). When on, the
   agent's discovery-eligible tools are hidden behind search.
2. **Per-tool eligibility** — whether a tool *may* be hidden by discovery:
   - **Always-on (never hidden):** basic native tools (the agent's curated `tools`
     allowlist) **and the cogmem suite** (fundamental to how the agent works).
   - **Discoverable (hidden when the switch is on):** the **fusion** and **maestro**
     suites, and **all upstream MCP** tools.

So with the switch on, an agent sees: its native tools + cogmem + the two search
meta-tools. Everything in fusion/maestro/MCP is searchable. (Amber: ~330 → ~37
advertised.)

### Effective decision per agent

```
base       = agent.progressive_discovery (nil => true)
wouldBe    = count of tools the agent would advertise with discovery OFF
forced     = threshold > 0 && wouldBe > threshold
effective  = base || forced          // an explicit OFF is overridden by the threshold
```

`threshold` default **120** (so 121 tools auto-enables it), `0` = disabled. Config
key under `tools.discovery.auto_threshold`, surfaced in the Config → Service card.
When `forced` fires we log it so an operator sees why discovery turned on.

## The 3-layer flow

- **Layer 1 — search** (`search_tools`): natural-language `query` → returns **names +
  one-line descriptions only** (no schemas). Optional `regex: true` switches the
  backend from BM25 (default) to regex, for when the LLM can't find something.
- **Layer 2 — inspect** (`get_tool_details`): `name` → returns the **full schema** for
  that one tool and **promotes** it (sets TTL > 0) so it becomes callable.
- **Layer 3 — execute**: the model calls the promoted tool normally.

Both meta-tools are **auto-suppressed** (not registered/advertised) when the agent's
effective discovery is off — no dead search tools in a small context.

### TTL / promotion lifecycle

- `get_tool_details` promotes a tool for `ttl` turns (existing `PromoteTools`).
- **Reset TTL on every use**: calling a promoted tool re-promotes it, so a long task
  never re-hides a tool mid-use. (New: `ExecuteWithContext` bumps TTL on hit.)
- `TickTTL` decays each turn; at 0 the tool goes hidden again.

## Config changes (`pkg/config/config.go` + doctor)

- **Add** `AgentConfig.ProgressiveDiscovery *bool` `json:"progressive_discovery,omitempty"`
  (nil => on), with helper `AgentProgressiveDiscovery(agentID) bool` — mirrors the
  existing `Cogmem *bool` / `CognitiveMemoryEnabled()` pattern (config.go:261).
- **Add** `ToolDiscoveryConfig.AutoThreshold int` `json:"auto_threshold"` (default 150,
  0 = disabled). Surfaced in the **Config → Service** card per request.
- **Remove** the global `ToolDiscoveryConfig.Enabled` switch (superseded by the
  per-agent toggle). Keep `TTL`, `MaxSearchResults`, `UseBM25`, `UseRegex` as engine
  params (UseBM25/UseRegex become backend availability, not per-agent gating).
- **Doctor migration** (`openclaw doctor --fix`): drop `tools.mcp.discovery.enabled`
  from existing configs; seed `auto_threshold: 150` if absent. Unreleased code, so no
  runtime shim — migrate then runtime reads the new shape only.

## Registry changes (`pkg/tools/registry.go`)

- Make **hidden orthogonal to suite-exempt**: `ToolEntry` gains a `Hidden bool`
  independent of `SuiteExempt` and `IsCore`. A fusion tool can be **hidden AND
  suite-exempt**.
- New registration entry points:
  - `RegisterHidden(tool)` — hidden, not exempt (MCP tools). *(exists; keep)*
  - `RegisterSuiteHidden(tool)` — hidden **and** suite-exempt (fusion/maestro when
    discovery on). *(new)*
  - `RegisterSuite` / `Register` unchanged (cogmem, native, and everything when
    discovery off).
- `ToProviderDefs`: a tool is advertised iff `IsCore || TTL > 0` **and** not `Hidden`
  (a promoted hidden tool has TTL > 0, so it shows). Keep the bare-ExternalName logic
  already there.
- `ExecuteWithContext`: on a successful resolve of a hidden tool, reset its TTL
  (re-promote on use).

## Tool classification (`pkg/agent/loop.go`, `loop_mcp.go`, `pkg/tools/providers.go`)

Registration decides visible vs hidden using `effective discovery` + eligibility:

| Tool class | discovery OFF | discovery ON |
|---|---|---|
| Native (curated `tools`) | Register | Register (always visible) |
| cogmem suite | RegisterSuite | RegisterSuite (never hidden) |
| fusion / maestro suite | RegisterSuite | **RegisterSuiteHidden** |
| upstream MCP | Register | **RegisterHidden** |

- Suite eligibility rule: a suite is discoverable iff `suite != "cogmem"`.
- Register `search_tools` + `get_tool_details` for the agent **iff** effective
  discovery is on and the agent allows them.
- Compute `wouldBe` count (native + all suites + MCP) to evaluate the auto-threshold
  before deciding.

## Meta-tools (`pkg/tools/search_tool.go`, `providers.go`)

- Replace the two model-facing `find_tools_bm25` / `find_tools_regex` with:
  - `search_tools(query, regex?)` — BM25 default; `regex:true` uses the regex engine.
    Returns `[{name, description}]` only.
  - `get_tool_details(name)` — returns the single full schema + promotes.
- Retire the `find_tools_*` names (unreleased; no alias kept). Regex/BM25 engines stay
  internal.
- `StaticToolDescriptors`: add `search_tools` / `get_tool_details` (default-off in the
  static list; they're registered dynamically only when discovery is on).

## WebUI changes

- **Tools catalog page** (`web/frontend/src/components/tools/tools-page.tsx`,
  `web/backend/api/tools.go`):
  - Rename the section so it's clear these are **always in the model's context**
    (e.g. "Always-On Tools"), not "Tools".
  - **Remove the greyed-out suite rows** (cogmem/fusion/maestro) — they're controlled
    by the agent suite switches; showing them here is noise.
- **Rationalize native defaults**: default **off** the byte-addressed edge-case tools
  — `file_read_bytes`, `file_edit_bytes`, `file_insert_bytes`, `file_delete_bytes`,
  `file_search_bytes` — most assistants work in text (line-addressed) tools. (Flip
  their default-allowed to false; still enableable per agent.)
- **Per-agent toggle**: add "Progressive tool discovery" (default on) to the agent
  editor (`web/frontend/src/components/agents/*`, agent config API).
- **Service card**: add the **auto-enable tool threshold** number field (default 150,
  0 = disabled) to `web/frontend/src/components/config/config-page.tsx`.

## Testing

- Registry: hidden+suite-exempt composability; `ToProviderDefs` excludes hidden,
  includes promoted; TTL reset on use.
- Classification: with discovery on, native+cogmem+meta advertised; fusion/maestro/MCP
  hidden; discovery off → all visible.
- Auto-threshold: agent with discovery explicitly off but wouldBe > threshold → forced
  on (and logged).
- `search_tools` (bm25 + regex path) returns names-only; `get_tool_details` returns one
  schema and promotes; execute works by the promoted name.
- Doctor migration drops `discovery.enabled`, seeds `auto_threshold`.

## Risks / open items

- **Threshold vs provider caps.** Default 150 sits above GPT's 128 function limit. With
  per-agent default-on this is moot, but an agent that turns discovery *off* with
  130–150 tools still exceeds GPT. Options: (a) accept it (rare, explicit opt-out), or
  (b) also keep a hard per-provider cap guard. You said the feature covers it — noting
  the residual edge.
- **Config location of `auto_threshold`.** Stored under `tools.discovery` for
  coherence with the other discovery params, but surfaced in the **Service** card UI as
  requested. Flagging the split.
- **Extra round-trip.** 3-layer adds one turn (search → details) before first use of a
  discovered tool — the accepted cost for token savings.
- **BM25 index freshness** over a now-larger hidden set (suites + MCP): confirm the
  BM25 cache invalidates on the registry version bump (it keys on `Version()` already).

## File-by-file task list

1. `pkg/config/config.go` — `ProgressiveDiscovery *bool` + helper; `AutoThreshold`;
   remove `Enabled`; schema/types.
2. `internal/.../doctor` — migration (drop `discovery.enabled`, seed `auto_threshold`).
3. `pkg/tools/registry.go` — `Hidden` flag, `RegisterSuiteHidden`, `ToProviderDefs`,
   TTL-reset-on-use.
4. `pkg/tools/search_tool.go` + `pkg/tools/providers.go` — `search_tools` /
   `get_tool_details`; retire `find_tools_*`; descriptors; file-bytes defaults off.
5. `pkg/agent/loop.go` + `loop_mcp.go` — effective-discovery calc (toggle + threshold),
   eligibility-based registration, meta-tool registration/suppression.
6. `web/backend/api/tools.go` + agent/config APIs — expose toggle + threshold; drop
   suite rows from catalog.
7. `web/frontend/src/components/tools/tools-page.tsx` — rename, remove greyed suites.
8. `web/frontend/src/components/agents/*` — per-agent toggle.
9. `web/frontend/src/components/config/config-page.tsx` — Service card threshold field.
10. Tests per the Testing section.
