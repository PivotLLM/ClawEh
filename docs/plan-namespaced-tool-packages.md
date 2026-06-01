# Plan: Namespaced Tool Packages

## Goal

Allow new tool packages to be added by creating a subdirectory and registering one line. The GUI, config, MCP server, and allowlists all adapt automatically — no additional wiring required.

## Current State

**Tool naming**: Flat snake_case, no namespacing. 23 built-in tools live as peers in `pkg/tools/`.

**Registration**: Spread across three call sites — `instance.go` (filesystem + session history), `loop.go:registerDynamicTools()` (web, hardware, message, skills, spawn), and `loop_session_tools.go` (compact_session, get_session_info). No single wiring point.

**Config `IsToolEnabled()`**: A large switch statement in `config.go:1371-1418` — hardcoded per tool. Adding a tool requires a new case.

**GUI catalog**: Hardcoded array in `web/backend/api/tools.go:36-163`. Adding a tool requires a new entry there plus a case in `applyToolState()`.

**Allowlists**: Already pattern-based (support `*`, prefix globs). The MCP host default list and per-agent default list are hardcoded string slices in `defaults.go`.

---

## Design

### Layer 1 — `ToolProvider` interface

New file `pkg/tools/provider.go`:

```go
type ToolProvider interface {
    Namespace() string                           // "files", "web", "session"
    Description() string                         // human label
    Category() string                            // GUI grouping
    ConfigKey() string                           // maps to config struct field
    Available(cfg *config.Config) (bool, string) // (ok, reason-if-not)
    Build(deps ToolDeps) []Tool                  // factory: receive runtime deps, return tools
}

// ToolDeps carries everything a tool package might need at construction time.
type ToolDeps struct {
    Config        *config.Config
    AgentConfig   *config.AgentConfig
    AgentID       string
    Workspace     string
    ArchiveDir    string
    MessageBus    *bus.MessageBus
    SubagentMgr   *SubagentManager
    ContextMgr    func(sessionKey string) *ContextManager
    // extend as needed
}
```

`ToolDeps` replaces the current ad-hoc closure pattern used to pass runtime state into tools during registration.

### Layer 2 — Package restructuring

Move tools into subdirectories. Each subdirectory implements `ToolProvider`.

```
pkg/tools/
  provider.go          ← ToolProvider interface + ToolDeps
  providers.go         ← compile-time list (see below)
  registry.go          ← unchanged
  base.go              ← unchanged
  files/               ← read_file → files_read, write_file → files_write, etc.
  web/                 ← web_search, web_fetch
  session/             ← get_session_messages, search_session_messages,
                          compact_session, get_session_info
  shell/               ← exec → shell_exec
  skills/              ← find_skills, install_skill
  spawn/               ← spawn
  hardware/            ← i2c, spi  (Available() returns false on non-linux)
  cron/                ← cron
  message/             ← message, send_file
```

**Naming convention**: `{namespace}_{action}` — e.g. `files_read`, `files_write`, `web_search`, `session_compact`. This reverses the current convention (`read_file` → `files_read`) but groups more naturally in alphabetical tool lists. Since the project is unreleased, renaming is clean.

**The compile-time list** in `providers.go` is the single place a new package is registered:

```go
// pkg/tools/providers.go
var Providers = []ToolProvider{
    files.Provider,
    web.Provider,
    session.Provider,
    shell.Provider,
    skills.Provider,
    spawn.Provider,
    hardware.Provider,
    cron.Provider,
    message.Provider,
}
```

Adding `pkg/tools/fubar` requires only implementing `ToolProvider` and appending `fubar.Provider` here.

### Layer 3 — Registration consolidation

Replace the three call sites in `instance.go`, `loop.go`, and `loop_session_tools.go` with a single loop:

```go
for _, p := range tools.Providers {
    if avail, _ := p.Available(cfg); !avail {
        continue
    }
    for _, tool := range p.Build(deps) {
        if agentCfg.IsToolAllowed(tool.Name()) {
            registry.Register(tool)
        }
    }
}
```

The `IsToolEnabled()` switch in `config.go` is replaced by a lookup against registered providers' `ConfigKey()` fields — no hardcoded cases.

The MCP allowlist defaults in `defaults.go` use `{namespace}_*` glob patterns instead of explicit tool names, so new tools in an already-registered namespace are automatically included.

### Layer 4 — GUI API becomes dynamic

`GET /api/tools` currently marshals a hardcoded `toolCatalog` array. It instead iterates `tools.Providers`, calls `Available()` on each, and returns the tool list built from `Build()` output plus provider metadata. The frontend already fetches this endpoint dynamically — no structural frontend changes needed, just removal of any hardcoded tool name references.

`PUT /api/tools/{name}/state` currently has a hardcoded `applyToolState()` switch. It instead looks up the tool's provider by namespace prefix and uses `provider.ConfigKey()` to identify which config field to update.

---

## Scope

| Area | Work | Risk |
|------|------|------|
| `ToolProvider` interface + `ToolDeps` | New file, no breakage | Low |
| Package restructuring + renames | Mechanical, large surface | Medium |
| Registration consolidation | Replaces 3 call sites | Medium |
| `IsToolEnabled()` → data-driven | Removes switch, touches config | Medium |
| `defaults.go` allowlists | Glob patterns, small edit | Low |
| GUI API dynamic catalog | Replaces hardcoded array | Low |
| `applyToolState()` switch | Config key lookup | Low |
| Tests | Tool names change throughout | High (volume) |

The highest-effort part is propagating tool renames through tests, config examples, and documentation.

---

## End State

After this change, adding `pkg/tools/fubar` with `fubar.Provider` and one line in `providers.go`:

- Tools appear in `GET /api/tools` automatically
- Config enable/disable works via `cfg.Tools.Fubar.Enabled`
- Per-agent allowlisting works via `fubar_*` glob
- MCP server exposes them if the namespace is in the host allowlist
- No changes to registry, MCP server, loop, instance, or GUI code required
