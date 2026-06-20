# Tool Provider Layer — Developer Guide

This document explains how ClawEh's tool-provider layer works and gives a spec
for adding a new tool module (a package of related tools). Registering a module
once makes its tools available to **both** transports:

- **Internal tool calling** — the agent loop builds a per-agent tool registry and
  passes the tools to the LLM.
- **The MCP server** — `pkg/mcpserver` is handed the same per-agent registries,
  so every tool is also exposed over MCP (`/mcp`).

It also feeds the WebUI tool catalogue. You write a module once; all three get it.

---

## 1. Architecture

```
your package (pkg/tools/<ns>/)        ← implements global.ToolProvider, BARE tool names
        │  RegisterTools(deps) []ToolDefinition
        ▼
pkg/global  (transport-neutral, stdlib-only)
        │  ToolProvider / ToolDefinition / Parameter / ToolCall / Result / Deps
        ▼
pkg/tools.NamespacedProvider("<ns>", pkg.GlobalProvider)   ← applies the "<ns>_" prefix
        │  bridges global.* into the legacy tools.Tool / tools.ToolProvider world
        ▼
pkg/tools registry  (RegisterProvider / GetProviders)
        ├──► agent loop  → per-agent *tools.ToolRegistry  → LLM tool calling
        ├──► pkg/mcpserver (WithAgentRegistries)           → MCP /mcp
        └──► web/backend/api/tools.go (Describe)           → WebUI catalogue
```

Key idea: a tool package is **transport-neutral and dependency-free** (it imports
only `pkg/global`, which itself imports only the standard library). The package
returns tools with **bare names** (`"read"`). The *aggregator* mounts the package
under a **namespace** (`"file"`), and the published tool name is
`"<namespace>_<bare>"` (`"file_read"`). Namespacing lives in the aggregator, not
the package, so packages never collide and can be remounted under a new namespace
without code changes.

### The aggregator (where modules are wired in)

`internal/gateway/tool_providers.go` is the **one file** that imports every tool
package and registers it:

```go
func registerToolProviders() {
    tools.RegisterProvider(tools.NamespacedProvider("file", files.GlobalProvider))
    tools.RegisterProvider(tools.NamespacedProvider("web", toolsweb.GlobalProvider))
    tools.RegisterProvider(tools.NamespacedProvider("session", session.GlobalProvider))
    // … one line per module …
}
```

To add an internal module you (a) add its import and (b) add one
`RegisterProvider(NamespacedProvider("<ns>", pkg.GlobalProvider))` line here.

---

## 2. Core types (`pkg/global/tools.go`)

```go
// A package implements this. deps is injected at build time.
type ToolProvider interface {
    RegisterTools(deps Deps) []ToolDefinition
}

// Everything needed to expose one tool. Name is BARE (no namespace).
type ToolDefinition struct {
    Name        string          // bare, e.g. "read"
    Description string
    Parameters  []Parameter     // declarative; the host derives JSON Schema
    RawSchema   map[string]any  // optional: a pre-built JSON Schema, used verbatim
    Hints       *ToolHints      // optional MCP annotations (read-only/destructive/…)
    Handler     ToolHandler

    SessionScoped bool          // needs ToolCall.Session populated
    Async         bool          // may use ToolCall.Notify / return Result.Async
    PrimaryOnly   bool          // primary agents only; excluded from sub-agents

    DefaultAllow *bool          // nil/false ⇒ DENY by default; Allow(true) ⇒ on
    Category     string         // GUI grouping (defaults to namespace)
    ConfigKey    string         // config gate key (defaults to "<ns>_<name>")
}

type ToolHandler func(call *ToolCall) (*Result, error)

// Per-call bundle. Ctx lives on the call (net/http.Request precedent).
type ToolCall struct {
    Ctx     context.Context
    Args    map[string]any
    AgentID string
    Session string                 // resolved session key ("" if none)
    Channel string
    ChatID  string
    Notify  func(*Result)          // async late-delivery; nil ⇒ host has no async path
}

// Structured result. Hosts that only want text read ForLLM.
type Result struct {
    ForLLM  string   // required: model-facing content
    ForUser string   // optional: routed to the human by the host
    Silent  bool
    IsError bool
    Async   bool
    Media   []string // media-store refs
    Err     error    // internal detail; not the control-flow signal
}

// Dependency injection. Capabilities a host may not offer are nilable.
type Deps struct {
    Cfg       any    // *config.Config in Claw (type-assert)
    AgentID   string
    Workspace string
    Spawn     any    // global.Spawner; nil ⇒ host cannot spawn sub-agents
    Host      any    // host-specific strong deps; Claw passes tools.ToolDeps
}

// Optional metadata a host may use for gating/grouping.
type HostMeta interface {
    Namespace() string
    Description() string
    Available(cfg any) (ok bool, reason string)
}
```

`Parameter` is the declarative source of truth for one input
(`Name/Type/Required/Description/Enum/Default/Minimum/…`). `ParametersToSchema`
renders `[]Parameter` into a JSON Schema object; you rarely call it yourself —
the bridge does. Use `RawSchema` only when migrating a tool that already has a
hand-written schema.

### Default-allow (security model)

`DefaultAllow` defaults to **deny**. A tool is exposed to clients only if it sets
`DefaultAllow: global.Allow(true)`, or an operator explicitly enables it via
config (`tools.tool_overrides["<ns>_<name>"] = true`). So new tools are off by
default until you opt in or the operator turns them on.

### Primary-only (sub-agent restriction)

`PrimaryOnly: true` marks a tool as available only to a **primary** (top-level)
agent — never to a spawned **sub-agent**. When a sub-agent's tool registry is
built, primary-only tools are excluded, and execution rejects them as
defense-in-depth (regardless of the per-agent allowlist). Set it for capabilities
a transient worker must not have:

- **`agent_spawn`** — prevents a sub-agent from spawning further sub-agents
  (no recursion).
- **`cron_schedule`** — a worker should not create/manage scheduled jobs.
- **cognitive-memory WRITE tools** (`cogmem_memory_create`, `cogmem_domain_update`,
  `cogmem_domain_create`, `cogmem_domain_archive`, `cogmem_domain_migrate`,
  `cogmem_memory_retire`, `cogmem_memory_confirm`, `cogmem_memory_forget`,
  `cogmem_consolidate`) — sub-agents get **read-only** memory: they share the
  primary's memory for background but cannot mutate it. The read tools
  (`cogmem_domain_get`, `cogmem_memory_search`, `cogmem_domain_list`,
  `cogmem_explain`, `cogmem_export`, `cogmem_status`) stay available.

How to set it, by layer:

- **Global-layer tools** (a `ToolProvider`): set `PrimaryOnly: true` on the
  `ToolDefinition`. The namespaced wrapper propagates it automatically.
- **Directly-registered tools** (implement `tools.Tool` and registered via
  `AgentLoop.RegisterTool`): implement the optional interface
  `IsPrimaryOnly() bool { return true }`.

Detection is uniform: `tools.IsPrimaryOnly(t)` returns true for either form
(tools that don't opt in are available to sub-agents).

---

## 3. Spec: adding a new tool module

### Step 1 — create the package

`pkg/tools/<ns>/` (short namespace, e.g. `weather`). Implement
`global.ToolProvider` on an exported zero-value, and optionally `global.HostMeta`:

```go
// pkg/tools/weather/global_provider.go
package weather

import (
    "fmt"

    "github.com/PivotLLM/ClawEh/pkg/global"
)

// GlobalProvider is the exported entry point the aggregator mounts.
var GlobalProvider globalWeatherProvider

type globalWeatherProvider struct{}

// global.HostMeta (optional but recommended): drives WebUI grouping + gating.
func (globalWeatherProvider) Namespace() string   { return "weather" }
func (globalWeatherProvider) Description() string  { return "Weather lookups" }
func (globalWeatherProvider) Available(cfg any) (bool, string) { return true, "" }

func (globalWeatherProvider) RegisterTools(deps global.Deps) []global.ToolDefinition {
    return []global.ToolDefinition{
        {
            Name:         "forecast",                 // published as weather_forecast
            Description:  "Return the forecast for a city.",
            Category:     "weather",
            DefaultAllow: global.Allow(true),         // omit/false ⇒ off by default
            Parameters: []global.Parameter{
                {Name: "city", Type: "string", Required: true, Description: "City name."},
                {Name: "days", Type: "integer", Description: "Days ahead (1-7).", Default: 1},
            },
            Handler: func(call *global.ToolCall) (*global.Result, error) {
                city, _ := call.Args["city"].(string)
                if city == "" {
                    return &global.Result{IsError: true, ForLLM: "city is required"}, nil
                }
                text, err := fetchForecast(call.Ctx, city) // your logic
                if err != nil {
                    return nil, err                        // bridge marks IsError + ForLLM
                }
                return &global.Result{ForLLM: text}, nil
            },
        },
    }
}
```

### Step 2 — register it in the aggregator

In `internal/gateway/tool_providers.go`:

```go
import "github.com/PivotLLM/ClawEh/pkg/tools/weather"

func registerToolProviders() {
    // … existing …
    tools.RegisterProvider(tools.NamespacedProvider("weather", weather.GlobalProvider))
}
```

That's it — `weather_forecast` is now available to agents (subject to the
allowlist), over MCP, and in the WebUI catalogue.

### Step 3 — add a test probe

`test.sh` / `tests/test_mcpserver.sh` exercise every MCP tool with the external
`probe` binary. Add your tool to the gateway test config's exposed tool list and
add a probe case — success for hermetic tools, a graceful-error probe for
network/hardware/LLM tools.

---

## 4. Rules & contracts

- **Enumeration must be deps-free.** `Describe()` (WebUI/MCP cataloguing) calls
  `RegisterTools(global.Deps{})` with a zero `Deps`. Return your full tool set
  and build handler **closures lazily** — never dereference `deps.Cfg`/`deps.Host`
  at the top of `RegisterTools` in a way that panics on nil. Construct real
  instances only when the dep is present (`c, _ := deps.Cfg.(*config.Config); if c != nil { … }`).
- **Bare names only.** Never prefix the namespace yourself; the aggregator does.
- **Return errors *and* set `Result`.** A returned `error` is for `errors.Is/As`;
  `Result.IsError`/`Result.ForLLM` are the model-facing outcome. The bridge folds
  a non-nil error into `IsError` + `ForLLM`, so returning `(nil, err)` is fine.
- **Recover strong host deps when needed.** Claw passes its rich
  `tools.ToolDeps` via `deps.Host`; recover it with
  `cd, _ := deps.Host.(tools.ToolDeps)` (gives `Workspace`, `AgentCfg`,
  `Dispatcher`, session closures, etc.). A pure module that needs none of that
  imports only `pkg/global`.
- **Session-scoped tools** set `SessionScoped: true` and read `call.Session`.
- **Async tools** set `Async: true`, return `Result{Async: true}` immediately,
  and deliver the late result via `call.Notify(...)` — but check `call.Notify != nil`
  first and degrade gracefully when the host has no async path.
- **Schema:** prefer `Parameters`; use `RawSchema` only to carry an existing
  hand-written schema during migration.

---

## 5. Importing as an external module

`pkg/global` is intentionally dependency-free (standard library + `context`
only), so the `ToolProvider`/`ToolDefinition` contract can be vendored or
imported by another codebase (e.g. Maestro/MCPFusion) without pulling in Claw.
A package written against `pkg/global` alone — no `pkg/tools`, `pkg/config`, etc.
— is portable: any host that can adapt `global.ToolProvider` into its own
registry can mount it. Claw's adapter is `tools.NamespacedProvider`; another host
would supply its own equivalent.

When a module needs Claw-specific dependencies, it reaches them through
`deps.Host.(tools.ToolDeps)` — which keeps the *portable* part (the tool
definitions and handlers' signatures) free of Claw imports while still letting
the handler bodies use Claw internals when running inside Claw.

### Spawning sub-agents from a module

A tool can launch sub-agent workers through the portable `global.Spawner` the
host injects into `Deps.Spawn`:

```go
sp, ok := deps.Spawn.(global.Spawner)
if ok && sp != nil {
    res, err := sp.Spawn(call.Ctx, global.SpawnRequest{
        Mode:    global.SpawnAndWait, // or SpawnCallback / SpawnDetached
        Task:    "summarize the attached log",
        Channel: call.Channel,
        ChatID:  call.ChatID,
        OnResult: call.Notify, // for SpawnCallback: re-inject the result on the channel
    })
    _ = res
    _ = err
}
```

The three modes are: `SpawnDetached` (fire-and-forget, result discarded),
`SpawnCallback` (fire-and-forget; the worker's result is delivered to
`OnResult` when it finishes — wire it to `call.Notify` to surface it on the
originating channel), and `SpawnAndWait` (run to completion and return the
worker's `Result` synchronously). `Spawner` lives in `pkg/global` (stdlib-only),
so an external module can depend on it without importing the rest of Claw; the
host supplies the concrete implementation.

---

## 6. Quick reference

| Thing | Location |
|---|---|
| Transport-neutral types | `pkg/global/tools.go` |
| Namespacing bridge | `pkg/tools/namespaced.go` (`NamespacedProvider`) |
| Registry | `pkg/tools/providers.go` (`RegisterProvider` / `GetProviders`) |
| **Aggregator (wire new modules here)** | `internal/gateway/tool_providers.go` |
| Per-module entry points | `pkg/tools/<ns>/global_provider.go` (`var GlobalProvider`) |
| Sub-agent restriction | `ToolDefinition.PrimaryOnly` / `tools.IsPrimaryOnly` (`pkg/tools/base.go`) |
| Host deps available to handlers | `pkg/tools` `ToolDeps` (via `deps.Host`) |
| MCP exposure | `pkg/mcpserver` (`WithAgentRegistries`) |
| WebUI catalogue | `web/backend/api/tools.go` (`Describe`) |
| Test probes | `test.sh`, `tests/test_mcpserver.sh` |
