# Agent SDK extraction plan

Status: **proposal, nothing implemented.** Revision 4. Written against ClawEh
`dev/0.6.5` (3476109) and `go.bison9.com/agent` branch `dev/0.0.1` (5bbfa3a,
version 0.2.0 in development).

Changes in revision 2, from the maintainer's review:

- The SDK repository keeps its name, `agent`. The module's root package is
  a deliberately small front door: the `Agent` interface, `agent.New` and
  functional options. The loop lives in a sub-package (section 3.1).
- A `models` package owns everything about models at run time: the list,
  purposes, priorities, selection, fallback, cooldown and what is serving
  now. ClawEh populates it and hands it to the agent (section 4.3).
- The accounting agent name grows to 64 characters and records gain the
  agent's friendly name (section 4.1).
- Cancellation is the first deliverable and is redesigned: a user interrupt
  stops the agent being talked to and **leaves its sub-agents running**; the
  agent gets a tool to cancel its own sub-agents (section 5, phase 1).
- The tool registry is a live object the host adds to and removes from at any
  time (section 4.4).
- The MCP host is split: a generic core moves to the SDK, ClawEh's policy
  stays (section 4.6).

Section 11 lists the decisions still open.

## 1. Goal

A new project that needs a custom agent writes roughly this and nothing more:

```go
logger.SetBackend(myLogger)

m := models.New()
m.AddProvider(modelspec.Provider{Name: "anthropic", Protocol: "anthropic", APIKey: key})
m.Add(modelspec.Model{ModelName: "sonnet", Model: "claude-sonnet-5-5", Provider: "anthropic"},
    models.For(models.Chat, 1))
m.Add(modelspec.Model{ModelName: "haiku", Model: "claude-haiku-5-5", Provider: "anthropic"},
    models.For(models.Chat, 2), models.For(models.Summarize, 1))

reg := toolkit.NewRegistry()
reg.Add(lookupOrder, refund)                          // toolspec definitions
reg.AddToolset(discovery.Hidden(crmTools))            // listed only once searched for

alice, _ := agent.New("alice",
    agent.WithName("Alice"),                          // friendly name: logs, accounting, notices
    agent.WithInstruction("You are Alice, a support agent for ..."),
    agent.WithModels(m),
    agent.WithTools(reg),
    agent.WithSessions(session.NewSQLite(dir)),
    agent.WithBeforeTool(approveRefunds),
)

run := alice.Run(ctx, "session-42", agent.UserText("Where is my order?"))
for ev, err := range run.Events() { /* stream to the user */ }

// from anywhere, at any time:
run.Interrupt()                     // stop this reply; sub-agents keep working
reg.Remove("refund")                // takes effect at the next model call
m.Select(models.Chat, "haiku")      // takes effect at the next turn
```

The host owns: loading config, building tools, a logger, and moving messages
between the user and the agent. Model fallback and cooldown, the tool loop,
discovery, context assembly and compaction, optional memory, cancellation
and accounting come from the SDK.

ClawEh becomes the first host. **No existing ClawEh behaviour, config key,
tool name or protocol shape changes** through the extraction. The one
deliberate exception is the cancellation work in phase 1, which adds a tool
and changes what `/cancel` does to waiting sub-agents; it is called out
there with its changelog entries.

## 2. What exists today (verified)

### The SDK repository (`go.bison9.com/agent`, branch `dev/0.0.1`)

| Package | Role | Imports |
| --- | --- | --- |
| `toolspec` | Tool contract: `ToolProvider`, `ToolDefinition`, `ToolCall`, `Result`, `Deps`, `Group`/`RevealTogether` | stdlib |
| `logger` | Logging seam, silent until a backend is set | stdlib |
| `accounting` | One record per LLM/API call, pricing table, `Identity{Agent, Project}` (each ≤16 characters, agent required) | `logger` |
| `spawnllm` | Provider clients (HTTP + CLI), `ProviderSpec`, `Worker` (a minimal tool loop), error classifier, `speech`, `imagegen` | `toolspec`, `accounting`, `logger` |
| `ctxengine` | Transcript, archive, assembly, eviction, compaction, `session_*` tools | `spawnllm`, `toolspec` |
| `cogmem` | Cognitive memory | `toolspec` |

Each package has an `imports_test.go` guard. No tags exist and, per the
maintainer, nothing consumes the repository yet.

### ClawEh

- **Still on the old modules.** `go.mod` requires
  `github.com/PivotLLM/{toolspec,spawnllm,ctxengine,cogmem}`.
- **Two tool loops.** `spawnllm.Worker.Run` is a ~40-line loop. ClawEh's real
  loop is `agent/llm_iteration.go` + `llm_dispatch.go` + `tool_exec.go`
  (`llmTurn`), which calls `provider.Chat` directly and never uses `Worker`.
- **Model policy** is `providers/` (1,684 lines): `FallbackChain`,
  `CooldownTracker`, `ProviderDispatcher`, `CreateProviderFromConfig`, the
  CLI-declined guard, `HumanProvider`. Imports `config`, `alerts`,
  `internal/childenv`, `internal/perms`, `logger`.
- **Model config** is `config.Provider` / `config.ModelConfig`
  (`config/providers.go`), the CLI table (`config/clis.go`),
  `CooldownConfig`, and run limits resolved in `agent/instance.go`.
- **The tool-provider layer** is the top of `tools/` (~2,600 non-test lines):
  `Tool`, `ToolResult`, `ToolProvider`/`SuiteProvider`/`ToolDeps`,
  `ToolRegistry`, the toolspec bridge (`namespaced.go`), context helpers.
  The registry already changes at run time: `Register`, `RemoveByPrefix`,
  `ReplaceByPrefix` (used on every upstream-MCP refresh) and a `Version`
  counter the BM25 cache keys on.
- **Progressive discovery exists**, inside that package: hidden entries and
  groups in `ToolRegistry`, `search_tools` / `get_tool_details`
  (`tools/search_tool.go`), BM25 (`utils/bm25.go`), deferred definitions
  (`ToProviderDefs`, `ToolResult.ToolReferences`,
  `spawnllm.DeferredToolsCapable`), wiring in `agent/loop_tools.go`.
- **The agent core** is `agent/` (17,950 non-test lines, 61 files) around
  `AgentLoop`. It imports 38 ClawEh packages.
- **The MCP host** is `mcpserver/` (1,850 lines) on `mark3labs/mcp-go`. It is
  how CLI models reach tools: each agent's `ToolRegistry` is published on two
  endpoints (`/mcp` bearer, `/internal` with a `session_token` argument), and
  the session token is given to the model in a prompt layer
  (`sessionTokenLayer`). It imports `bus`, `config`, `routing`, `agenttoken`,
  `tools/agents`, `mcpserver/acl`, `internal/tokenhash`.

### Cancellation today

| What | Behaviour today | Source |
| --- | --- | --- |
| How a user interrupts | Only by typing `/cancel`. `processSessionMessage` intercepts it; `sessionState.cancel()` cancels the turn context with `errCancelledByUser` and drops the session's queue. | `agent/session_dispatch.go` |
| Programmatic cancel | None. No exported method, no turn handle. | — |
| Device protocol | Handles `connect`, `health`, `chat.send`, `node.event`, `agents.list`, `chat.history`. No abort method. | `channels/device/server.go` |
| WebUI chat | Typing `/cancel` works, like any text channel. It produces two messages: "Cancelled the current request." (the command's reply, `commands/cmd_cancel.go`) and "⚠️ Cancelled by /cancel. Some steps may have completed — ask me to continue if needed." (the reply owed to the interrupted message, `decideTurnReply`). There is no dedicated Stop control. | `commands/cmd_cancel.go`, `agent/turn_reply.go` |
| Sub-agent, `callback` mode | Runs on `context.Background()`: **already survives `/cancel`**. | `tools/agents/subagent.go` `runRecord` |
| Sub-agent, `wait` mode | Runs on the turn's context: **killed by `/cancel`**. | `SubagentManager.Run` |
| Cancelling a sub-agent | **Impossible.** The agent tools are `agent_spawn`, `agent_status`, `agent_list`; no cancel function is kept for a running task, and task status is `running`/`done`/`error` only. | `tools/agents/global_provider.go`, `taskstore.go` |
| Restart | The supervisor relaunches tasks still marked running. | `SuperviseOnce` |
| Asked agent (`agent_message`) | The asked turn is not stopped when the asker stops (only the forum's asks stop theirs). | `agent/ask.go` |

Not verified: what a Maestro dispatch (`RunSync`) does when the calling turn
is cancelled. Phase 1 establishes it before deciding.

## 3. Target layout

### 3.1 The top level: `agent`, kept small

The repository and module stay `go.bison9.com/agent` (`BisonNine/agent`);
`adk` is Google's name and is not used. Everything else in the module is
part of the agent or helps it, so the agent itself is the module's root
package.

**Rule: the root package is a front door, not a workshop.** It holds only:

- the `Agent` interface;
- the constructor `agent.New(id string, opts ...Option) (Agent, error)`;
- `Option` and the `With...` functions (Go functional options), which is
  how *everything* is supplied: models, tools, sessions, memory, limits,
  clock, and every callback;
- the small values a caller needs to talk to an agent (`UserText`, run
  options), and type aliases for the public types defined below it
  (`Run`, `Event`, `Outcome`, the callback signatures), so a host imports
  one package for ordinary use.

It holds no loop, no provider code, no session logic. A guard test caps it:
the root package starts no goroutine and has no file other than
`agent.go`, `options.go`, `aliases.go` and their tests.

```go
type Agent interface {
    ID() string
    Name() string
    Run(ctx context.Context, session string, msg Message, opts ...RunOption) Run
    Interrupt(session string) (running bool, dropped int)
    Tasks() []TaskInfo
    CancelTask(taskID string, cause error) error
    Cancel(cause error)            // every turn and task of this agent
    Tools() *toolkit.Registry
    Models() *models.Set
    Close(ctx context.Context) error
}
```

**Where the struct lives.** The loop needs the agent's settings, and the
root needs the loop to build an agent. If the struct were defined in the
root, the loop would have to import the root, and Go refuses that cycle.
So:

- `agent/hook` (a leaf) defines the callback signatures, `Event`, `Outcome`,
  `Run`, `Message`, and the small interfaces the loop depends on (`Memory`,
  `SessionStore`). No logic.
- `agent/loop` defines the concrete struct that *is* the agent
  (`loop.Agent`, with its settings in `loop.Spec`) and the whole turn
  engine: iterations, tool execution, retries, session queues, task
  contexts, interrupt and cancel.
- The root's options are `func(*loop.Spec)`; `agent.New` applies them,
  validates, and returns `loop.New(spec)` as the `Agent` interface.

Dependencies run one way: `agent` → `agent/loop` → `agent/hook`, and
`agent/loop` → `models`, `toolkit`, `discovery`, `ctxengine`. Nothing
imports the root.

**No separate runner.** Revisions 1–3 had a `runner` package, after Google's
design. With the agent as the object the host holds, running, interrupting
and cancelling are methods on it; the session queues a runner would own are
inside `agent/loop`. A host with many agents (ClawEh) holds many `Agent`
values that share one models catalogue.

ClawEh already has its own package named `agent`, so the few ClawEh files
that use both import the SDK under an alias.

### 3.2 Packages

New packages are marked NEW.

```
toolspec      stdlib                           tool contract
logger        stdlib
accounting    logger                           (identity widened, see 4.1)
spawnllm      toolspec accounting logger       provider clients (Worker retired, see 4.8)
modelspec     stdlib                           NEW  provider + model definitions, validation, CLI table
models        modelspec spawnllm logger        NEW  the live model object: lists, purposes, selection,
                                                    fallback, cooldown, clients (see 4.3)
toolkit       toolspec logger                  NEW  Tool, Result, live Registry, Toolset
discovery     toolkit toolspec                 NEW  hidden-tool index, search, deferred loading
ctxengine     spawnllm toolspec
cogmem        toolspec
agent/hook    toolkit spawnllm(types)          NEW  callback signatures, events, run handle, small interfaces
agent/loop    hook models toolkit discovery    NEW  the agent struct and the turn engine
              ctxengine accounting
agent (root)  loop hook models toolkit         NEW  Agent interface, New, options, aliases
mcphost       toolkit + mark3labs/mcp-go       NEW  serves a Registry over MCP (for CLI models)
```

Every new package gets an `imports_test.go` guard. The module rule stays:
**the SDK never imports a host.** `mcphost` is the only package that pulls in
`mcp-go`; a host that uses no CLI model never links it.

## 4. Package designs

### 4.1 `accounting` — identity

```go
type Identity struct {
    Agent   string // stable id, required, ≤ 64 characters
    Name    string // friendly name, optional, ≤ 64 characters
    Project string // optional, default "default", ≤ 64 characters
}
```

- `MaxNameLength` 16 → 64, which is ClawEh's agent-id limit and holds a
  UUID.
- `Name` is written to each record as `agent_name`, and `"-"` when unset,
  like every other unreported value. It is a label only: grouping and the
  pricing lookup keep using `Agent`.
- How ClawEh fills it: `Agent` is the normalized agent id
  (`config.NormalizeAgentID`); `Name` is `AgentConfig.DisplayName()`.
  For a clone, `Agent` is its **source's** id so a sub-agent's spend rolls up
  to the agent that caused it, and `Name` is the clone label
  (`Alice (clone 1a2b3c4d)`, today's `AgentInstance.Label`). A fresh
  temporary agent uses its UUID and its given name.
- In the SDK, the id given to `agent.New` and `agent.WithName` feed it; the host never builds an
  `Identity` by hand for turns. Direct `speech`/`imagegen` calls still take
  one.
- Display names can be longer than 64 or contain anything. `Normalize`
  refuses an over-long `Agent` (an id is the host's contract) but **truncates**
  `Name` with an ellipsis rather than refusing a launch over a label.

### 4.2 `modelspec` — shared model configuration

The one definition of "a provider" and "a model" that ClawEh, the SDK and new
hosts import.

Moves from ClawEh `config/`:

- `Provider` and `ModelConfig` **with JSON tags unchanged**, so `config.json`
  (`providers`, `model_list`) parses as now. ClawEh keeps
  `config.Provider = modelspec.Provider` aliases, the pattern `global` and
  `providers` already use.
- Vision constants, `IsCLIProtocol`, `TaskOnlyProtocol`, protocol
  validation, `ModelConfig.Validate`, `HasCredentials`, reserved body keys.
- `config/clis.go`: `CLIAgent`, `CLIArgs`, `CLIEnv`, `BypassArgs`.
- `CooldownConfig` and defaults.
- A `Set` (`Providers`, `Models`) with `Lookup(alias)`, `Resolve(names...)`,
  `Validate()`, `LoadFile`/`Parse`.
- `Limits` (`max_tokens`, `context_window`, `thinking_level`, `no_tools`,
  `vision`, `request_timeout`) computed from a model plus host defaults: the
  generic half of `resolveRunLimits`.

Stays in ClawEh: secret references (resolved before values reach modelspec),
the `Store`, model reference sites/rename/prune, `task_models`, human-agent
rules. The `human` protocol is registered by ClawEh through
`modelspec.RegisterProtocol`; the SDK does not know it.

### 4.3 `models` — one object for everything about models

Today model handling is spread over ClawEh: the catalogue in `config`,
clients in `ProviderDispatcher`, health in `CooldownTracker`, ordering in
`FallbackChain`, per-agent chains in `agent/instance.go`
(`resolveAgentCandidates`, `resolveWorkerCandidates`), the summarization
chain in `agent/context_manager.go`, the per-session `/model` choice in
`agent/loop_session_state.go`, and six task-model lists read in several
places. The `models` package replaces all of the run-time part (and the
`dispatch` package of earlier revisions). ClawEh's job becomes: populate
it from config, and ask it questions.

**Two levels, because the data has two scopes.** A single flat list cannot
represent ClawEh as it is:

- *Install-wide:* the definitions, the HTTP/CLI clients, and health. A
  model that is out of credit is out of credit for every agent, and the
  compaction path must skip it too.
- *Per agent:* which models it uses for which purpose and in what order.
  Alice and Bob may share a model at different priorities, and the same
  model may serve chat for one and summarization for another.

So there is one **`Catalog`** per process and one **`Set`** per agent, which
is a view onto the catalogue. `models.New()` returns a catalogue with a
default set, so a single-agent host never sees the distinction.

```go
// Catalog: install-wide. Definitions, clients, health.
c := models.New(models.WithCooldown(policy), models.WithObserver(obs))
c.AddProvider(p modelspec.Provider) error
c.Add(m modelspec.Model, uses ...Use) error       // uses apply to the default set
c.Remove(name string) error
c.Replace(providers []modelspec.Provider, list []modelspec.Model) (Diff, error) // reload, atomic
c.Get(name string) (Info, bool)
c.Health() []Health                               // available / parked until / last reason / failures
c.ResetHealth(names ...string)                    // clear cooldown (the WebUI button)
c.RegisterClient(name string, cl spawnllm.LLMProvider) // ready-made client (ClawEh's human provider)
c.Set(id string) *Set                             // per-agent view, created on first use

// Set: one agent's models, by purpose and priority.
s.Assign(purpose Purpose, name string, priority int, opts ...AssignOption) error
s.Unassign(purpose Purpose, name string)
s.Reorder(purpose Purpose, names []string)
s.List(purpose Purpose) []Entry                   // in priority order, with health and settings
s.Select(purpose Purpose, name string, scope ...Scope) error
s.Selected(purpose Purpose, scope ...Scope) Entry
s.Reset(purpose Purpose, scope ...Scope)          // back to the top-priority model
s.Current(purpose Purpose, scope ...Scope) Serving // {Selected, Serving, Since, Reason, Attempts}
s.Begin(purpose Purpose, scope ...Scope) *Chain   // a pinned chain for one turn
```

Points of design, each answering something in the current code:

- **Purpose** is an open string type with constants the SDK itself uses:
  `Chat`, `Summarize` (compaction), `Memory` (cogmem consolidation),
  `Worker` (sub-agents). A host adds its own; ClawEh adds `vision`, `stt`,
  `tts`, `image`, `text`, `classify` for its task models. A purpose with no
  models falls back by a declared rule (`Worker` → `Chat`, as
  `WorkerCandidates` does today), never silently.
- **Priority belongs to the assignment, not the model.** It is an integer,
  lower first, ties in insertion order. ClawEh's config stores ordered
  lists, so it assigns list position as the priority; nothing in
  `config.json` changes.
- **Per-assignment settings.** An assignment can carry opaque settings
  (`models.WithSettings(v)`), which is where ClawEh's `image_api` and
  `tts_defaults` ride.
- **Selection has a scope.** `/model` in ClawEh is per session and is
  persisted in the session's compaction state. `Select` takes an optional
  scope (a session key); without one it sets the agent's default. Selection
  is held in memory and written through an optional `SelectionStore`
  interface, which the loop implements over the session store, so the
  choice survives restarts as it does now. A selection whose model is
  removed reverts to the default and reports it.
- **A turn is pinned.** `Begin` returns a `Chain`: the selected model
  first, then the rest in priority order, fixed for the turn so a
  multi-step tool chain never changes model half-way (today's `turnModel`).
  `chain.Chat(ctx, id, req)` runs the call with cooldown skips and
  fallback, marks success and failure, and reports each hop. The chain also
  exposes the pinned model's limits (context window, max tokens, thinking,
  `no_tools`, vision), which the loop needs before the first call.
- **"What is serving now"** is `Current`: the selected model, the one that
  actually answered last, since when, and why they differ (cooldown, auth,
  rate limit, timeout). That feeds the "model X failed, trying Y" notice,
  `/model`, the WebUI and Check Up from one source.
- **Events, not alerts.** `Observer` gets `Parked`, `Recovered`,
  `FellBack`, `Exhausted`. ClawEh's observer raises the same alerts with
  the same incident keys (`ALERTS.md` unchanged); another host logs them or
  ignores them.
- **Locking.** The catalogue and each set publish immutable snapshots
  behind an atomic pointer; writers build a new snapshot under one mutex.
  No lock is ever held across a model call, an observer callback, or a
  `SelectionStore` write. Health counters have their own lock, as the
  cooldown tracker does now. Race tests cover add/remove/select/replace
  during calls.
- **Reload is `Replace`.** ClawEh populates the catalogue at boot, but its
  config can be saved at any time. `Replace` diffs against the running
  catalogue: unchanged models keep their client and health (cooldowns
  already survive a reload today and must keep doing so), changed ones get
  a new client, and a replaced or removed client is closed only after its
  in-flight calls end (today's `providerCalls` guarantee, which moves here).
- **Isolated CLI clients** (`GetIsolated`: an empty workspace and no bypass
  flags for fresh temporary agents, classifiers and task models) become
  `chain.Isolated(workspace)`.

What moves in from ClawEh `providers/`: the factory (`CreateProviderFromConfig`,
`compatOpts`, `responsesOpts`), `ProviderDispatcher`, `FallbackChain` and
its types, `CooldownTracker`/`CooldownPolicy`, `providerCalls`.

| Dependency today | Replacement |
| --- | --- |
| `*config.Config` | `modelspec` values passed to `Add`/`Replace` |
| `alerter` + `alerts.Incidents` | `models.Observer` |
| `internal/childenv` | `models.WithBaseEnv`; the SDK default is a minimal allowlist, never the full environment |
| `internal/perms` | `models.WithFileModes` |
| CLI-declined wording | host-supplied formatter (ClawEh names the agent and model) |

**What does not move.** The config file and its shape; secret references;
validation on save; the model reference sites (rename follows a model
through every list, delete is refused while a list names it, a dangling
name is pruned); human-agent rules; per-agent task-model *grants*; the
WebUI pages. Those are about ClawEh's configuration document, not about
models at run time. The `TaskModelsConfig.Entries(use)` rule stays: it
becomes the one place ClawEh reads the lists in order to assign them.

What ClawEh deletes or shrinks once this exists: `providers/` (to the human
provider, the observer and aliases), `resolveAgentCandidates`,
`resolveWorkerCandidates`, `selectCandidates`, the active-model cache,
`resolveCompressModelChain` and its client resolution, the candidate
plumbing in `ToolDeps` (`Provider`, `Dispatcher`, `Fallback`, `Candidates`,
`CandidateResolver` become one `*models.Set`), and the task-model host's
candidate and cooldown code.

### 4.4 `toolkit` — tools and the live registry

Moves from ClawEh `tools/` top level: `Tool`, `AsyncExecutor`,
`SessionScoped`, `ToolResult`, `Refusal`, the toolspec bridge and
namespacing, schema helpers, per-call context helpers, `ExecuteUnattended`,
tool stats, `untrusted`.

**The registry is the host's handle on an agent's tools, for the agent's
whole life:**

```go
reg := toolkit.NewRegistry()
reg.Add(defs ...toolspec.ToolDefinition) error        // refuses a duplicate name
reg.Remove(names ...string) int
reg.Replace(prefix string, defs []toolspec.ToolDefinition) (added, removed int) // one atomic swap
reg.AddToolset(ts Toolset) (remove func())             // a named group, removable as a unit
reg.AddProvider(ns string, p toolspec.ToolProvider, deps toolspec.Deps) (remove func())
reg.Names() []string
reg.Version() uint64
reg.Subscribe(func(Change)) (unsubscribe func())       // Change{Added, Removed []string}
```

Rules, each with a test:

- **Safe from any goroutine, at any time,** including mid-turn.
- **A change takes effect at the next model call.** Each model call takes
  one snapshot; a turn in progress sees the new list on its next iteration.
  A call to a tool removed since the model saw it returns an ordinary tool
  error ("tool X is no longer available"), never a crash.
- **Shared or per-agent.** One registry may back several agents; an agent
  may also be given a filtered view (`reg.View(allow AllowChecker)`), which
  is how ClawEh's per-agent grants sit on shared providers.
- **Subscribers** are how the discovery index, the MCP host's
  `tools/list_changed` notification and ClawEh's WebUI catalogue stay in
  step without polling.
- **Cost, stated in the docs:** the tool list is part of the prompt-cache
  prefix, so every add or remove of a *listed* tool invalidates the cache for
  that agent. Hidden (discovery) tools do not have this cost, which is the
  reason to put a large or changing set behind discovery.

`Toolset` (`Name()`, `Tools(ctx)`) covers dynamic groups. Tools that live in
**another process** come in as a toolset too: ClawEh's upstream-MCP client
(`mcp/`, 1,826 lines) already does this with `ReplaceByPrefix`. It can move
to the SDK later as `toolkit/mcpclient`; it is not in the first cut.

Stays in ClawEh's `tools` package: `ToolProvider.Available(cfg)`, `ToolDeps`,
the process-wide `RegisterProvider` list, grant policy (`IsToolAllowed`,
suites, `shell_exec` never matched by `*`). ClawEh keeps aliases
(`tools.Tool = toolkit.Tool`, …) so the ~80 tool files need no edits.

### 4.5 `discovery` — progressive tool discovery

Separate so a host can use it with any registry, or not at all.

- `Index`: hidden-tool corpus with groups and reveal-together, rebuilt on
  the registry's change notification; BM25 and regex search.
- `discovery.Hidden(toolset, opts...)` marks a toolset hidden;
  `AlwaysShown(patterns...)` pins (today's `always_shown_namespaces` and the
  Maestro entry-tool pin); `MaxResults`.
- `search_tools` and `get_tool_details`, **same names, descriptions and
  result text as now**.
- The deferred-definition contract, today split across
  `ToolRegistry.ToProviderDefs`, `sentToolDefs` and spawnllm.

Preserved: a hidden tool may always be *called*; discovery decides only what
is *listed*; the MCP host never applies it.

### 4.6 `mcphost` — should the MCP server move?

Recommendation: **split it.** Move the mechanism, keep ClawEh's policy.

The reason to move anything: with a CLI model the vendor CLI runs its own
tool loop, so the SDK's loop never executes a tool. The only way such a model
reaches host tools is an MCP server. If that stays entirely in ClawEh, an
SDK agent on `claude-cli` or `codex-cli` has no tools, and "model fallbacks"
quietly lose tools whenever the chain reaches a CLI model.

| Moves to `agent/mcphost` | Stays in ClawEh |
| --- | --- |
| Serving a `toolkit.Registry` per agent over streamable HTTP; catalogue diffing and `tools/list_changed` | Listener address, endpoint paths `/mcp` and `/internal`, TLS, the Host check |
| The session-token store: issue, resolve, revoke, per-turn scope (depth, reply chat, ask chain) | Service tokens (`servicetoken`), `agenttoken`, token hashing, the ACL policy (`mcpserver/acl`) |
| The `session_token` argument injection and the dispatch path that runs `BeforeTool`/`AfterTool` | Publishing ForUser text, breadcrumbs and async results on the bus; `stream_tool_activity` |
| An `Authenticator` interface (`token → agent, session, scope`) | ClawEh's authenticator over session + service tokens |
| A helper that produces the prompt layer and the CLI's MCP arguments for a turn | `set-mcp.sh`, `claw install` wiring, the `mcp__claw__` prefix and realm (fixed identifiers) |

Consequences to design for:

- The engine treats a CLI model as "tools are served, not looped": for such
  a turn it issues a session token, scopes it, and revokes it at turn end.
  Cancel must also revoke, so a cancelled CLI turn's token stops working.
- Tool callbacks fire on the `mcphost` dispatch path, so audit and redaction
  are uniform across HTTP and CLI models. Today ClawEh duplicates that logic
  in `mcpserver/tools.go`.
- It moves late (phase 7), after the engine. Until then ClawEh's
  `mcpserver` keeps working against the `toolkit` aliases.

### 4.7 `agent`, `agent/loop`, `agent/hook`

Modelled loosely on Google's Agent Development Kit for Go
(`google.golang.org/adk` v1.10.0): callbacks around the turn, the model
call and the tool call that can short-circuit; an event iterator; toolsets.
Different on purpose: functional options instead of a config struct, plain
Go types rather than `genai`, a models object with fallback instead of one
model, no separate runner, and a run handle with interrupt and cancel.

Options (all in the root package, each a one-line function over
`loop.Spec`):

```go
agent.WithName(string)                    agent.WithDescription(string)
agent.WithInstruction(string)             agent.WithInstructionLayers(func(TurnInfo) []ctxengine.Layer)
agent.WithModels(m)                       // a *models.Catalog (its default set) or a *models.Set
agent.WithTools(*toolkit.Registry)        agent.WithAllow(toolkit.AllowChecker)
agent.WithSessions(SessionStore)          agent.WithMemory(Memory)
agent.WithContext(...ctxengine.Option)    agent.WithLimits(Limits)
agent.WithProject(string)                 agent.WithClock(Clock)
agent.WithPlugin(Plugin)                  // a named bundle of callbacks

agent.WithBeforeTurn(fn)   agent.WithAfterTurn(fn)
agent.WithBeforeModel(fn)  agent.WithAfterModel(fn)  agent.WithOnModelError(fn)
agent.WithBeforeTool(fn)   agent.WithAfterTool(fn)   agent.WithOnToolError(fn)
agent.WithOnFallback(fn)   agent.WithOnCompaction(fn)
agent.WithOnTextDelta(fn)  agent.WithOnInterrupt(fn) agent.WithOnEvent(fn)
```

A callback option may be given more than once; hooks run in the order
given. `agent.New` fails on a missing model set, a purpose with no models
and no fallback rule, or a duplicate tool name, so a wrong setup is refused
at construction rather than on the first turn.

**Callbacks.** A non-nil replacement skips the step (as in Google's kit). The
right column is the ClawEh behaviour that becomes an implementation of the
hook.

| Hook | Can | ClawEh implements with it |
| --- | --- | --- |
| `BeforeTurn` | replace the reply, refuse | human-agent turns, whispers, session reset, pending-turn marker |
| `AfterTurn` | rewrite the reply | empty-reply text, image notes, single-shot discard, deferred compaction |
| `BeforeModel` | edit the request, answer instead | low-steps note, media resolution, strict alternation, reasoning backfill, dumps |
| `AfterModel` | edit the response | `emitFinish`, spend, usage, generated images, reasoning channel |
| `OnModelError` | supply a response, change the error | failover and billing text |
| `OnFallback` | observe (fed by `models`) | "model X failed, trying Y", logs |
| `BeforeTool` | edit args, answer instead, refuse | audit row, breadcrumb, approval, depth and turn-chat scoping |
| `AfterTool` | edit the result | secret redaction, ForUser/media publishing, vision describe, stats |
| `OnToolError` | supply a result | refusals logged at WARN |
| `OnCompaction` | observe | "Alice is summarising…" and eviction notices |
| `OnTextDelta` | observe | stream coalescer |
| `OnInterrupt` | observe | the `/cancel` reply, withdrawn requests to people |
| `OnEvent` | observe | progress heartbeat, everything else |

**Events** from `Run.Events()`: `TurnStarted`, `ModelCall`, `TextDelta`,
`ToolCallStarted`, `ToolCallFinished`, `Fallback`, `Compaction`, `Image`,
`TaskStarted`, `TaskFinished`, `TurnFinished{Outcome}`. `Outcome` is today's
bus contract: `ok`, `error`, `cancelled`, `empty`.

**Kept from `llmTurn`, behaviour unchanged:** model chosen once per turn and
sticky; fallback per call with cooldown skips, shared with compaction;
timeout retry with backoff and jitter; context-overflow retry that compacts
and lowers `max_tokens`; parallel tools with per-tool timeout; the
repeated-batch stop; the empty-reply poke; `tool_choice: none` on the last
step with tools still declared; prompt-token calibration; async tools and
late results; CLI providers as one call.

### 4.8 `spawnllm.Worker`

A second, weaker loop once `agent` exists. Since nothing uses the repository
yet, **delete it** when the engine lands rather than keep a wrapper. Check
Maestro and MCPFusion first: they still use the old `PivotLLM/spawnllm`
module and I could not see those repositories from this session.

## 5. Cancellation (priority; ships first, in ClawEh)

Built in ClawEh now, in the shape the SDK will have, then moved with the
engine. It does not wait for the extraction.

### 5.1 Model

Three things can be stopped, and they are separate:

| Unit | What it is | Who may stop it |
| --- | --- | --- |
| **Turn** | One reply being produced in a session | The person chatting (`/cancel`, a stop control), the host, shutdown |
| **Task** | A sub-agent or task-model job, running under a task id | The agent that owns it; the host |
| **Queue** | Messages waiting behind the running turn | Dropped with an interrupt, as now |

Tasks are **not children of the turn's context.** Every task runs on its own
context, registered with the agent under its id and its owner. That one
change gives both requirements: an interrupt cannot reach a task, and a task
can be cancelled by id.

```go
run.Interrupt()                         // the turn only; tasks keep running
run.Cancel(cause)                       // the turn and every task it started (host use)
alice.Interrupt(sessionID) (running bool, dropped int)
alice.Tasks() []TaskInfo
alice.CancelTask(taskID, cause) error   // cascades to that task's own tasks
alice.Cancel(cause)                     // everything of this agent (shutdown, /cancel all)
```

### 5.2 Interrupt: stop the reply, leave sub-agents alone

When a person interrupts:

1. The in-flight model call is aborted: HTTP request cancelled, CLI process
   group killed, the turn's MCP session token revoked.
2. Tools running in the turn get a cancelled context. The engine waits
   `Limits.CancelGrace`, then abandons them and records a synthetic
   "interrupted" result for each, so the stored transcript never holds a
   tool call without a result.
3. Queued messages for the session are dropped and counted (today's reply:
   how many were skipped).
4. **`callback` tasks:** untouched. Same as today.
5. **`wait` tasks: detached, not killed.** This is the behaviour change. The
   turn stops waiting; the task keeps running on its own context, its record
   changes from `wait` to `callback`, and its result is delivered the way a
   callback result is (a pointer into the owner's main conversation). The
   interrupt reply says so: "Stopped. 2 background tasks are still running."
6. Exactly one `TurnFinished{Outcome: cancelled}`; `AfterTurn` runs on a
   context that is not cancelled so the host can persist and reply.
7. Cause is recorded (`interrupted by user`, `shutdown`, `deadline`,
   `superseded`). A shutdown-cancelled turn stays pending for restart
   recovery, as now; an interrupted one does not.

Unchanged: an agent asked through `agent_message` is not stopped when its
asker is interrupted, a request to a person is withdrawn, and forum runs are
unaffected (they are owned by the forum, not the turn).

### 5.3 The agent cancels its sub-agents

A new tool beside `agent_spawn`, `agent_status`, `agent_list`:

```
agent_cancel { "uuid": "<task id>" }      // one task
agent_cancel { "all": true }              // every running task this agent owns
```

- **Ownership:** only the task's owner (`TaskRecord.OwnerAgentID`, compared
  with `config.SameAgentID`; a clone acts as its source). Another agent's
  task is "unknown", the same answer `agent_status` gives.
- **Effect:** the task's context is cancelled with the same steps as 5.2
  items 1–2, the clone is released, and **the task's own tasks are cancelled
  too** (a cancelled worker has nobody left to receive their results).
- **Record:** status becomes `cancelled`, a new terminal value next to
  `done` and `error`; the run marker is cleared and the task leaves the live
  set **before** the context is cancelled, so neither the supervisor nor a
  restart relaunches it. Partial output the worker had produced is kept in
  the results file, marked as incomplete.
- **Result to the caller:** how many tasks were cancelled and which were
  already finished. No callback fires for a cancelled task; the canceller
  already knows.
- **Task-model jobs** (`kind` text/image/tts/stt) share the records and are
  cancellable the same way.
- **Idempotent:** cancelling a finished or already-cancelled task is not an
  error.

Granting (decided): **any agent granted `agent_spawn` gets `agent_cancel`.**
It is registered together with `agent_spawn` and is not a separate entry in
the `tools` list, the Agents page or `CODING_TOOLS`; `deny_tools` can still
remove it. It is the first tool granted by implication, so the rule is
written down where the grant rule lives (`AgentConfig.IsToolAllowed`,
`CLAUDE.md` "Tool grants") and shown on Check Up's per-agent tool list and in
the capabilities golden file, so nobody finds a tool the list does not name
without an explanation. Clones and sub-agents inherit it with `agent_spawn`.

### 5.4 What the person types

Decided:

- **`/cancel` stops only the agent the person is talking to:** the running
  reply and the messages queued behind it. Every sub-agent keeps running,
  including one the reply was waiting on (5.2).
- **`/cancel all` stops that agent and everything under it:** the reply, the
  queue, and every running task the agent owns, cascading to their own
  tasks (the same path as `agent_cancel {"all": true}`).

Replies name what happened, per the "name the thing" rule:
"Alice: stopped. 2 background tasks are still running — /cancel all stops
them too." and "Alice: stopped, and cancelled 2 background tasks."

`/cancel` already works wherever text can be sent, the WebUI chat included
(revision 2 wrongly called the WebUI a gap). What does not exist is a
control that needs no typing:

- a **Stop button** in the WebUI chat while a reply is in progress;
- an **abort request** on the device listener, which matters for voice
  clients such as the R1, where saying or typing "/cancel" is awkward. The
  protocol document lists `aborted` as a chat state but no inbound abort
  method is handled, and I found none in the client library ClawEh imports;
  the method name must be confirmed against the OpenClaw Gateway protocol.

Both are optional conveniences on top of the command and need approval
individually (D4).

**One reply per cancel (decided).** Today one `/cancel` produces two
messages: the command's own reply (`commands/cmd_cancel.go`) and the reply
owed to the interrupted message (`decideTurnReply`), plus one more per
queued message that required a reply (`publishCancelledReplies`). After
phase 1 a cancel sends exactly one message to the chat it was typed in,
saying what was stopped, how many queued messages were dropped and which
tasks are still running. The interrupted and dropped messages are still
closed with `Outcome: cancelled` for the turn contract, but without text of
their own when they belong to the same chat. A waiting asker (another agent)
and a message from a different chat still get their own cancelled reply,
since nobody else would tell them.

**Devices.** The device socket is not blocked while a reply is pending:
`chat.send` is acknowledged at once (`{runId, status:"started"}`) and the
reply arrives later as events, so a client can send another request on the
same connection at any time. Two ways to cancel follow from that:

- *Today, with no protocol change:* a second `chat.send` whose message is
  `/cancel`. It reaches the same `/cancel` path as any text channel. One
  defect to fix: the listener tracks a single `currentRun` per connection
  and the second `chat.send` overwrites it, so the cancelled run's closing
  events are attributed to the `/cancel` run id and the original run never
  gets its own terminal event. Phase 1 keeps runs by id so the interrupted
  run ends with `chat` state `aborted` (and `agent` lifecycle end) under
  its own id.
- *A dedicated request:* an abort method carrying the run id, for clients
  that have a stop button or a hardware key rather than a text box. The
  OpenClaw Gateway this listener imitates acknowledges runs and documents
  an `aborted` chat state, which implies such a method, but I could not
  confirm its name or payload from this session; it must be checked against
  the protocol before it is added (D4). Whether a given client ever sends
  it is up to the client: the R1 and the Android app would each need
  checking.

### 5.5 What changes for operators (changelog)

- `Added`: the `agent_cancel` tool; task status `cancelled` in
  `agent_status` / `agent_list`; (if approved) the WebUI Stop control and the
  device abort method.
- `Added`: `/cancel all`.
- `Changed`: a cancel sends one message instead of two or more.
- `Fixed`: on the device listener, a run interrupted by a `/cancel` sent
  during it now ends under its own run id with state `aborted`.
- `Changed`: `/cancel` no longer stops a sub-agent the reply was waiting on;
  the sub-agent finishes and its result arrives as a background result. The
  reply names the tasks still running and points to `/cancel all`, which
  restores the old effect and also stops background tasks.
- `Changed`: an agent granted `agent_spawn` now also has `agent_cancel`
  (add it to `deny_tools` to withhold it).
- `docs/webui-test-plan.md`, the MCP probe list (`tests/test_mcpserver.sh`)
  and the capabilities golden file gain the new tool; Check Up's per-agent
  tool list shows it.

## 6. What stays in ClawEh

Everything that is product, not engine: `bus`, `channels`, `routing`,
`commands`, the turn-reply contract, asks/whispers/turn slots, human agents,
forum, classifiers, task models, character cards, cron, voice and media,
audit, alerts, restart recovery, retention, message tokens, `ContextBuilder`,
`cogmemhost`, the config `Store`, permissions, the WebUI, and the policy half
of the MCP host (4.6).

`AgentLoop` remains ClawEh's host object and shrinks to: route inbound →
resolve the `AgentInstance` → `Agent.Run` with ClawEh's callbacks → publish
the reply. `agentreg` and the sub-agent task store stay in ClawEh at first;
the SDK agent supplies the task contexts and ids they register with.

## 7. Phases

Each phase lands on its own with `make test` green in both repositories.
Phases 2–5 must leave `tests/capabilities/testdata/effective.golden`
unchanged.

**Phase 0 — adopt the module.**
Widen `accounting.Identity` and add
`Name` (4.1). ClawEh swaps the four `PivotLLM/*` requirements for the one
module, updates the alias shims and `cycle_guard_test.go`, and passes an
identity on every `Chat`/`Transcribe`/`Synthesize`/`Generate` call. Decide
whether ClawEh writes `accounting.log` (location, permissions area, backup,
Check Up row) or leaves accounting on the logger for now.
*Exit:* ClawEh builds on `go.bison9.com/agent`, no behaviour change; records
carry id and friendly name; `CLAUDE.md` "Shared modules" rewritten. Needs a
tagged SDK version from the maintainer.

**Phase 1 — cancellation in ClawEh.** Independent of phase 0; may run first
or in parallel.

- 1a. Task contexts: every sub-agent and task-model job runs on a context
  registered by id and owner. Establish and record what Maestro dispatch
  does on a cancelled turn.
- 1b. `Interrupt` as an exported method with a turn handle; `/cancel`
  reimplemented on it; `wait` tasks detach (5.2).
- 1c. `agent_cancel`, the `cancelled` status, supervisor and restart
  handling (5.3).
- 1c'. `/cancel all` (5.4).
- 1d. WebUI Stop button and device abort, if approved (5.4).

*Exit:* tests for each numbered rule in 5.2 and 5.3 on the fake clock,
including: interrupt during a model call, during a tool, with a `wait` task
(task finishes and reports), cancel of a task with its own task, cancel then
restart (not relaunched), CLI model (process gone, token revoked). `make
test`, the MCP integration script with the new probe, and the WebUI browser
suite if 1d is included.

**Phase 2 — `modelspec`.** Move the types; ClawEh aliases them; fixture
configs marshal byte-identically before and after.

**Phase 3 — `models`.** In two steps, because it changes more than it moves.

- 3a. Move factory, client cache, fallback and cooldown into `models` as the
  `Catalog`, behind the Observer/BaseEnv seams, with ClawEh `providers/`
  reduced to aliases. No behaviour change.
- 3b. Add `Set` (purposes, priorities, selection, `Current`, `Begin`) and
  move ClawEh's chain building, `/model` state, summarization chain and
  task-model candidate code onto it. ClawEh populates the catalogue at boot
  and calls `Replace` on reload.

*Exit:* CLI command lines and environments in the golden file identical;
cooldown alerts unchanged (`ALERTS.md` untouched); cooldowns still survive
a reload; `/model` choices survive a restart; a table test replays today's
fallback cases (cooldown skip, exhausted chain, context limit, sticky turn)
against the new package with identical attempt sequences.

**Phase 4 — `toolkit`.** Move the tool core and give the registry the API in
4.4 (`Add`/`Remove`/`Replace`/`Subscribe`/`View`); ClawEh's `Register*` and
`ReplaceByPrefix` calls become thin wrappers. *Exit:* `tests/test_mcpserver.sh`
untouched and passing; new tests for add/remove mid-turn.

**Phase 5 — `discovery`.** Extract index, search, meta-tools and deferral.
*Exit:* identical tool lists with discovery on and off.

**Phase 6 — the engine (`agent`, `agent/loop`, `agent/hook`).** The large one.

- 6a. In ClawEh, introduce the callback signatures and events and make
  `llmTurn` call them, with today's behaviour as the callback
  implementations. No package moves. This is where the 38 imports are cut.
- 6b. Move the host-free engine files, with phase 1's cancellation, to
  `agent/loop`; add `agent/hook` and the root package (interface, `New`,
  options) with its size guard; `AgentLoop` builds each agent with
  `agent.New`. Delete `spawnllm.Worker`.
- 6c. An example host in the SDK (`examples/chat`): two models, two tools,
  stdin/stdout, Ctrl-C as interrupt. It is the acceptance test for
  section 1 and stays under ~100 lines.

*Exit:* `make test`, every integration script, the WebUI browser suite on a
dev instance, and a real-device check with the R1 for streaming order.

**Phase 7 — `mcphost`.** Split `mcpserver` per 4.6; the example host gains a
CLI model with tools. *Exit:* section 8 of `tests/test_mcpserver.sh` and the
opt-in real-CLI tests pass.

**Later, if a second host needs them:** `agentreg` and the task store as SDK
packages; the upstream-MCP client as `toolkit/mcpclient`.

Size: phases 0 and 2–5 are mostly mechanical. Phase 1 is moderate and
self-contained. Phase 6 is the bulk (the engine files are ~4,500 lines and
the seam work touches most of `agent/`). I have not estimated calendar time.

## 8. Testing

- Every moved package brings its tests; ClawEh keeps the tests of its
  callback implementations.
- New in the SDK: one scripted fake provider (ClawEh has several ad hoc
  ones) driving table tests for fallback order, cooldown skip, both retry
  kinds, repeated-batch stop, discovery reveal, registry changes mid-turn,
  and the cancellation rules.
- Guards: `imports_test.go` per package; ClawEh's `cycle_guard_test.go`
  asserts no SDK package imports ClawEh.
- The capabilities golden file is the oracle for phases 2–5; integration
  scripts and the browser suite for phases 1, 6 and 7.

## 9. Documentation

- ClawEh `CLAUDE.md`: "Shared modules", "Providers", the tool-provider,
  discovery and sub-agent passages change as packages move, in the same
  commits. Phase 1 adds the cancellation rules.
- `manual/`: `/cancel` and the new tool (agents section); the Stop control
  and device abort if added; `docs/device-protocol.md`.
- `docs/tool-providers.md`: new import paths, the registry API.
- SDK: a README per package and `CHANGELOG.md` entries.

## 10. Risks

- **Phase 6 rewrites the hottest path under a no-behaviour-change rule.**
  Mitigation is 6a: restructure in place, behind the existing suite, before
  anything moves.
- **Detached `wait` tasks change what an agent sees.** A turn that was
  interrupted while waiting gets the result later as a background result,
  in a new turn. Prompts and the `agent_spawn` description must say so, or
  the model will be surprised by a result it believes it never got.
- **Runaway workers.** After phase 1 `/cancel` alone no longer stops a
  waiting worker; `/cancel all` does, and the `/cancel` reply says so.
- **Callback ordering.** Redaction before storage, audit before execution,
  images saved before the assistant message is stored. Hooks run in
  registration order; ClawEh registers them in one place with a test that
  pins the order.
- **Tool-list churn and the prompt cache** (4.4): a host that adds and
  removes listed tools often will pay for it on every turn.
- **`models` growing beyond run-time state.** Configuration rules
  (reference sites, grants, human agents) must stay out of it, or ClawEh's
  config validation ends up split across two repositories.
- **Alias sprawl** across `global`, `providers`, `tools`, `config`. Plan a
  cleanup pass after phase 6 replacing aliases with direct imports.
- **Two repositories moving together.** ClawEh needs pseudo-versions or a
  `replace` during development; `main` never carries a `replace`.
- **Reload.** `models.Catalog.Replace` and agent rebuilds must keep the
  guarantee that a replaced provider is closed only after its in-flight
  calls end.

## 11. Decisions

Settled in review:

- The repository stays `agent`. The root package is the small front door
  (interface, `agent.New`, functional options); the struct and the loop are
  in `agent/loop` (3.1).
- A `models` package owns models at run time; ClawEh populates it and
  passes it to the agent (4.3).
- Accounting agent name 64 characters, plus a friendly name.
- Interrupt leaves sub-agents running; the agent can cancel its own.
- The registry is live.
- Any agent granted `agent_spawn` gets `agent_cancel` (5.3).
- `/cancel` stops only the agent being talked to; `/cancel all` also cancels
  all of its tasks (5.4).
- A cancel sends one message, not one per affected message (5.4).

Still open:

- **D1. Clone spend:** attribute a sub-agent's accounting records to its
  source agent with the clone label as the name (recommended, 4.1), or to
  the clone's own id.
- **D13. No separate runner** (3.1): running and cancelling as methods on
  the agent (recommended), or keep a `runner` package.
- **D14. `models` in two levels** (4.3): a process-wide catalogue plus a
  per-agent set (recommended; shared cooldown with per-agent ordering needs
  it), or one flat object.
- **D15. `modelspec` separate from `models`:** keep the definitions in a
  stdlib-only leaf so ClawEh's `config` does not import run-time machinery
  (recommended), or fold them into `models`.
- **D2. MCP host split** as in 4.6 (recommended), or leave all of it in
  ClawEh and accept that SDK agents on CLI models have no tools.
- **D4. Controls that need no typing:** a WebUI Stop button and a dedicated
  device abort request in phase 1d, or defer them. `/cancel` as text
  already works in both places, on a device as a second `chat.send`.
- **D6. Detached `wait` results:** deliver as a background result that
  starts a new turn (recommended, consistent with `callback`), or store
  silently for `agent_status`.
- **D7. `CLAW_COOLDOWN_*` env tags** on a struct that moves to the SDK: keep
  the tags there (inert for other hosts) or keep a ClawEh-side struct.
- **D8. Session storage in the SDK:** ctxengine's SQLite store behind an
  interface, plus an in-memory one for tests and small hosts (recommended).
- **D9. Memory in the first cut:** include the optional `agent.Memory`
  interface from the start (recommended; the loop's observe/recall calls
  are few).
- **D10. "The package that defines our tool provider interface":** I read
  this as ClawEh's `tools` core, since `toolspec` is already in the module.
  Correct me if it meant something else.
- **D11. Branching:** phase 1 on the current dev branch; each later phase on
  the dev branch of the version it ships in; no long-lived SDK branch.
