# Agent SDK extraction plan

Status: **proposal, nothing implemented.** Written against ClawEh `dev/0.6.5`
(3476109) and `go.bison9.com/agent` branch `dev/0.0.1` (5bbfa3a, version 0.2.0
in development). Section 10 lists the decisions that need the maintainer
before work starts.

## 1. Goal

A new project that needs a custom agent should be able to write roughly this
and nothing more:

```go
cfg, _ := modelspec.LoadFile("models.json")          // providers + models
sdklog.SetBackend(myLogger)                          // one logger

alice, _ := agent.New(agent.Config{
    Name:        "alice",
    Instruction: "You are Alice, a support agent for ...",
    Models:      cfg.Resolve("sonnet", "gpt-fallback"), // ordered: fallback chain
    Tools:       []toolspec.ToolDefinition{lookupOrder, refund},
    Toolsets:    []toolkit.Toolset{discovery.Wrap(crmTools)}, // hidden until searched
    Callbacks:   agent.Callbacks{BeforeTool: approveRefunds},
})

r, _ := runner.New(runner.Config{Agent: alice, Sessions: session.NewSQLite(dir)})

run := r.Run(ctx, "session-42", agent.UserText("Where is my order?"))
for ev, err := range run.Events() { /* stream to the user */ }
// elsewhere: run.Cancel(agent.CancelledByUser)
```

The host owns only: loading config, building tools, a logger, and moving
messages between the user and the runner. Everything else (model fallback and
cooldown, the tool loop, discovery, context assembly and compaction, optional
memory, cancellation, accounting) comes from the SDK.

ClawEh becomes the first and most demanding host of that SDK. **No ClawEh
behaviour, config key, tool name or protocol shape changes** as a result; the
capabilities golden file (`tests/capabilities`) must not change in any phase.

## 2. What exists today (verified)

### `go.bison9.com/agent` (branch `dev/0.0.1`)

| Package | Role | Imports |
| --- | --- | --- |
| `toolspec` | Tool contract: `ToolProvider`, `ToolDefinition`, `ToolCall`, `Result`, `Deps`, `HostMeta`, `Group`/`RevealTogether` | stdlib |
| `logger` | Logging seam, silent until a backend is set | stdlib |
| `accounting` | One record per LLM/API call, pricing table, `Identity{Agent, Project}` | `logger` |
| `spawnllm` | Provider clients (HTTP + CLI), `ProviderSpec`, `Worker` (a minimal tool loop), error classifier, `speech`, `imagegen` | `toolspec`, `accounting`, `logger` |
| `ctxengine` | Transcript, archive, assembly, eviction, compaction, `session_*` tools | `spawnllm`, `toolspec` |
| `cogmem` | Cognitive memory | `toolspec` |

Each package has an `imports_test.go` guard. No tags exist.

### ClawEh

- **ClawEh does not consume the consolidated module yet.** `go.mod` still
  requires `github.com/PivotLLM/{toolspec,spawnllm,ctxengine,cogmem}`.
- **There are two tool loops.** `spawnllm.Worker.Run` is a ~40-line loop with a
  progress callback. ClawEh's real loop is `agent/llm_iteration.go` +
  `llm_dispatch.go` + `tool_exec.go` (`llmTurn`), which calls
  `provider.Chat` directly and never uses `Worker`. All the valuable behaviour
  lives in the second one.
- **Model policy** is `providers/` (1,684 lines): `FallbackChain`,
  `CooldownTracker`, `ProviderDispatcher` (alias → cached client),
  `CreateProviderFromConfig`, the CLI-declined guard, `HumanProvider`. It
  imports `config`, `alerts`, `internal/childenv`, `internal/perms`, `logger`.
- **Model config** is `config.Provider` and `config.ModelConfig`
  (`config/providers.go`), the CLI table (`config/clis.go`: base args,
  `BypassArgs`, env), `CooldownConfig`, and per-model run limits resolved in
  `agent/instance.go` (`resolveRunLimits`, `resolveAgentCandidates`).
- **The tool-provider layer** ("the package that defines our tool provider
  interface") is the top of `tools/` (~2,600 non-test lines): `Tool`,
  `ToolResult`, `ToolProvider`/`SuiteProvider`/`ToolDeps` (`provider.go`),
  `ToolRegistry` (`registry.go`), the toolspec bridge (`namespaced.go`),
  context helpers (`base.go`, `turn_chat.go`, `turnscope.go`), schema opening,
  refusals. It imports `config`, `providers`, `agenttoken`, `constants`,
  `logger`, `utils`, `tools/untrusted`.
- **Progressive discovery already exists**, inside that same package:
  hidden/core entries and groups in `ToolRegistry`, `search_tools` /
  `get_tool_details` (`tools/search_tool.go`), BM25 in `utils/bm25.go`,
  deferred definitions (`ToProviderDefs` sets `Deferred`, results carry
  `ToolReferences`, `spawnllm.DeferredToolsCapable`), wiring and pins in
  `agent/loop_tools.go` (`registerProviderTools`, `discoveryPins`).
- **The agent core** is `agent/` (17,950 non-test lines, 61 files) around
  `AgentLoop`, a single struct that mixes the turn engine with everything
  ClawEh-specific. It imports 38 ClawEh packages, including `bus`, `channels`,
  `commands`, `forum`, `classifier`, `ccv3`, `voice`, `media`, `mcp`.
  `agentreg` (1,821 lines) is already generic by design (the loop hands it
  `BuildFunc`/`RetireFunc`/`InsertedFunc`) but imports `config`, `routing`,
  `cogmemhost`, `internal/perms`.
- **Cancellation today is only the `/cancel` chat command.**
  `processSessionMessage` intercepts it, `sessionState.cancel()` cancels the
  running turn's context with `errCancelledByUser` and drops the queue. There
  is no exported method to cancel a turn, no turn handle, and no cancel on the
  device protocol or the HTTP API. `ProcessDirect` callers can only cancel
  their own context. Whether a cancelled turn's background sub-agent tasks
  stop is **not verified** and must be established in phase 5.
- **Interception points today are hard-wired**, not callbacks: tool audit
  (`recordToolCallAudit`), breadcrumbs, the fallback notifier
  (`providers.FallbackNotify`), `emitFinish`/`recordSpend`, stream coalescer,
  compaction-start hook, eviction notice, secret redaction, tool-result cap,
  vision describe, generated-image handling, the low-steps note, the
  repeated-batch stop, the empty-reply poke.

### Two facts that constrain the design

1. **`accounting.Identity.Agent` is limited to 16 characters and is required
   on every `Chat`.** ClawEh agent ids are up to 64 characters and temporary
   agents have UUID ids (36). ClawEh cannot adopt the consolidated module
   until this is settled (decision D2).
2. **`spawnllm` is below everything.** Model config that `spawnllm` should not
   know about (aliases, limits, fallback order) must sit in a package above or
   beside it, never inside it.

## 3. Target layout

All in the `go.bison9.com/agent` module. New packages in **bold**.

```
toolspec     stdlib                         tool contract (exists)
logger       stdlib                         (exists)
accounting   logger                         (exists)
spawnllm     toolspec accounting logger     provider clients (exists; Worker retired, see 4.6)
modelspec    stdlib                         NEW  provider + model definitions, validation, CLI table
dispatch     modelspec spawnllm logger      NEW  factory, client cache, fallback chain, cooldown
toolkit      toolspec logger                NEW  Tool, Result, Registry, namespacing, Toolset
discovery    toolkit toolspec               NEW  hidden-tool index, search, deferred loading
ctxengine    spawnllm toolspec              (exists)
cogmem       toolspec                       (exists)
agent        dispatch toolkit discovery     NEW  Agent, callbacks, events, the turn engine
             ctxengine modelspec toolspec
             (cogmem only through an interface)
runner       agent                          NEW  sessions' turn queues, Run handles, Cancel
```

Package names are proposals (D1). Each new package gets an `imports_test.go`
guard like the existing ones, and the module rule stays: **the SDK never
imports a host.**

## 4. Package designs

### 4.1 `modelspec` — shared model configuration

Purpose: the one definition of "a provider" and "a model" that ClawEh, the SDK
and any new host import.

Moves from ClawEh `config/`:

- `Provider` and `ModelConfig` **with their JSON tags unchanged**, so
  ClawEh's `config.json` (`providers`, `model_list`) parses exactly as now.
  ClawEh keeps `config.Provider = modelspec.Provider` type aliases, the same
  pattern `global` and `providers` already use for toolspec/spawnllm.
- Vision constants, `IsCLIProtocol`, `TaskOnlyProtocol`, protocol validation
  (`protocolProblem`), `ModelConfig.Validate`, `HasCredentials`,
  reserved request-body keys.
- `config/clis.go`: `CLIAgent`, `CLIArgs`, `CLIEnv`, `BypassArgs`.
- `CooldownConfig` and its defaults.
- A `Set` type (`Providers []Provider`, `Models []Model`) with `Lookup(alias)`,
  `Resolve(names ...string) []Ref`, `Validate()`, and a `LoadFile`/`Parse`
  helper for hosts that keep models in their own file.
- Per-model run limits (`max_tokens`, `context_window`, `thinking_level`,
  `no_tools`, `vision`, `request_timeout`) as a `Limits` value computed from a
  model plus host defaults: the generic half of `resolveRunLimits`.

Stays in ClawEh `config/`: secret references (`env:`/`file:`, resolved before
values reach modelspec), the `Store`, model reference sites/rename/prune,
`task_models`, human-agent rules, round-robin, everything agent-related.

Env tags: `CooldownConfig` carries `env:"CLAW_COOLDOWN_*"` tags. They are
fixed identifiers ClawEh depends on; either the tags stay on the moved struct
(inert for other hosts) or ClawEh keeps a thin struct that maps onto
modelspec's (D3).

The `human` protocol is ClawEh policy. modelspec treats protocols as an open
set with a registry (`RegisterProtocol`), so ClawEh registers `human` and the
SDK does not know it.

### 4.2 `dispatch` — clients, fallback, cooldown

Moves from ClawEh `providers/`:

- `CreateProviderFromConfig` + `compatOpts`/`responsesOpts` → `dispatch.NewClient(model, provider, opts)`.
- `ProviderDispatcher` → `dispatch.Clients` (cache by alias, `Get`,
  `GetIsolated(alias, workspace)`, `Flush`).
- `FallbackChain`, `FallbackCandidate`, `FallbackAttempt`, `FallbackResult`,
  `FallbackExhaustedError`, `ResolveCandidatesWithLookup`.
- `CooldownTracker`, `CooldownPolicy`, `Snapshot`, `Clear`.

Dependencies to cut:

| Today | Replacement |
| --- | --- |
| `*config.Config` in the dispatcher | `modelspec.Set` (ClawEh passes `cfg.ModelSet()`) |
| `alerter.Alerter` + `alerts.Incidents` in the cooldown tracker | `dispatch.Observer` interface (`ModelParked`, `ModelRecovered`); ClawEh's implementation raises the same alerts with the same incident keys |
| `internal/childenv` (CLI environment allowlist) | `ClientOptions.BaseEnv func(protocol) []string`; ClawEh passes childenv. Default in the SDK is a minimal allowlist, never the full process environment |
| `internal/perms` (response-log file modes) | `ClientOptions.FileModes` |
| CLI-declined guard and its wording | moves; the user-facing sentence is produced by a host-supplied formatter (ClawEh names the agent and model per its WebUI rules) |

`HumanProvider` stays in ClawEh and is injected as a ready client
(`Clients.Register(alias, client)`).

### 4.3 `toolkit` — the tool-provider layer

Moves from ClawEh `tools/` top level: `Tool`, `AsyncExecutor`,
`SessionScoped`, `ToolResult` and constructors, `Refusal`/`IsExpectedRefusal`,
`ToolRegistry` (minus discovery, see 4.4), `NamespacedProvider` and the
toolspec bridge, `ToolToSchema`/`OpenObjectProperties`, the per-call context
helpers (`WithToolContext`, `WithSessionKey`, `WithReplyChat`/`TurnChat`),
`ExecuteUnattended`, tool stats.

New: `Toolset` (ADK's term): `Name()` + `Tools(ctx) ([]Tool, error)`, so a
host can hand the agent a dynamic group (an MCP server, a Fusion service).
`NamespacedProvider` becomes one implementation of it.

`ToolProvider.Available(cfg *config.Config)`, `ToolDeps` (which carries
`*config.Config`, `providers.*`, `AgentServices`, `Messenger`) and the global
`RegisterProvider` registry are ClawEh's host layer. They stay in ClawEh's
`tools` package, which shrinks to that plus aliases
(`tools.Tool = toolkit.Tool`, …) so the ~80 tool files and `mcpserver` need
no edits beyond imports where they want them.

Dependencies to cut: `config` (the allow checker is already an interface,
`ToolAllowChecker`), `providers` (only for `ToProviderDefs` → use spawnllm's
type directly), `agenttoken`/`constants` (move the two uses to the host side),
`utils` (BM25 goes to discovery; the rest is small helpers to copy),
`tools/untrusted` (moves with toolkit as `toolkit/untrusted`, it is a leaf).

Grant policy (`IsToolAllowed`, suites, `shell_exec` never matched by `*`)
stays in ClawEh. The SDK registry enforces whatever `AllowChecker` it is
given, exactly as `ExecuteWithContext` does today.

### 4.4 `discovery` — progressive tool discovery

A separate package so a host can use it with any registry, or not at all.

- `Index`: the hidden-tool corpus (name, description, group,
  reveal-together), versioned; BM25 (from `utils/bm25.go`) and regex search.
- `Wrap(toolset, opts) toolkit.Toolset`: marks a toolset's tools hidden.
  Options: `AlwaysShown(patterns...)` (today's `always_shown_namespaces` and
  the Maestro entry-tool pin), `MaxResults`.
- The two meta-tools, `search_tools` and `get_tool_details`, **same names,
  descriptions and result text as now** (they are in the capabilities golden
  file and in model-facing prompts).
- The deferred-definition contract: hidden tools are sent `Deferred` to
  providers reporting `DeferredToolsCapable`, otherwise revealed by adding
  them to the list after a result references them. This logic is currently
  split between `ToolRegistry.ToProviderDefs`, `sentToolDefs`
  (`agent/loop_turn.go`) and spawnllm; discovery owns the first two.
- `ToolRegistry` keeps a small extension point (`Visibility` interface) so the
  registry itself no longer knows about hidden entries.

Rule preserved: a hidden tool may always be *called*; discovery only decides
what is *listed*. The MCP host never applies discovery.

### 4.5 `agent` and `runner` — the SDK

Modelled loosely on Google ADK for Go (`google.golang.org/adk` v1.10.0:
`llmagent.Config`, `runner.Runner.Run` returning `iter.Seq2[*session.Event, error]`,
slices of before/after callbacks). Kept from ADK: the agent/runner split,
config-struct constructors, callbacks that can short-circuit, an event
iterator, toolsets. Deliberately different: plain Go types instead of `genai`
types (we are provider-neutral), an ordered model list instead of one model,
and an explicit run handle with `Cancel`.

```go
package agent

type Config struct {
    Name, Description string
    Instruction       string              // or
    InstructionLayers func(TurnInfo) []ctxengine.Layer  // ClawEh's PromptLayers

    Models  []modelspec.Ref   // ordered fallback chain; index 0 is primary
    Clients *dispatch.Clients // shared across agents (shared cooldown)

    Tools    []toolspec.ToolDefinition
    Toolsets []toolkit.Toolset
    Allow    toolkit.AllowChecker // nil = everything given is allowed

    Context ContextOptions // window, compaction, eviction (ctxengine options)
    Memory  Memory         // optional; cogmem satisfies it through an adapter
    Limits  Limits         // max iterations, tool timeout, turn timeout, tool-result cap

    Callbacks Callbacks
    Plugins   []Plugin     // a Plugin is a named Callbacks bundle (ADK-style)
    Clock     Clock
}
```

**Callbacks.** Each is a slice-capable hook; returning a non-nil replacement
skips the step (ADK semantics). The right-hand column is the ClawEh behaviour
that becomes an implementation of the hook instead of code in the loop.

| Hook | Can | ClawEh implements with it |
| --- | --- | --- |
| `BeforeTurn(ctx, *Turn)` | replace the reply, refuse | human-agent turns, whispers prepended, session reset, pending-turn marker |
| `AfterTurn(ctx, *Turn, *Reply)` | rewrite the reply | empty-reply text, image notes, pending marker cleared, single-shot discard, deferred compaction |
| `BeforeModel(ctx, *ModelRequest)` | edit request, answer instead | low-steps note, media resolution, strict alternation, reasoning backfill, request dump |
| `AfterModel(ctx, *ModelRequest, *ModelResponse)` | edit response | `emitFinish`, spend, usage, generated images, reasoning channel |
| `OnModelError(ctx, *ModelRequest, error)` | supply a response, change the error | failover rendering, billing text |
| `OnFallback(ctx, passed []Attempt, next Candidate)` | observe | the "model X failed, trying Y" heads-up, logs |
| `BeforeTool(ctx, *ToolCall)` | edit args, return a result instead, refuse | audit row, breadcrumb, approval, depth and turn-chat scoping |
| `AfterTool(ctx, *ToolCall, *toolkit.Result)` | edit the result | secret redaction, ForUser/media publishing, vision describe, stats |
| `OnToolError(ctx, *ToolCall, error)` | supply a result | refusal logging at WARN |
| `OnCompaction(ctx, CompactionEvent)` | observe | "Alice is summarising…" notice, eviction notice |
| `OnTextDelta(ctx, string)` | observe | stream coalescer → streaming channels |
| `OnEvent(ctx, Event)` | observe | everything else (progress heartbeat) |

**Events** (what `Run.Events()` yields): `TurnStarted`, `ModelCall`,
`TextDelta`, `ToolCallStarted`, `ToolCallFinished`, `Fallback`, `Compaction`,
`Image`, `TurnFinished{Outcome}`. `Outcome` is today's bus contract:
`ok`, `error`, `cancelled`, `empty`. A host that only wants the text calls
`run.Wait()`.

**What the engine keeps from `llmTurn`, unchanged in behaviour:**

- model chosen once per turn and sticky across iterations;
- the fallback chain per call with cooldown skips, shared with compaction;
- timeout retry with backoff and jitter; context-overflow retry that
  compacts and lowers `max_tokens`;
- parallel tool execution with per-tool timeout;
- the repeated-identical-batch stop and steer; the empty-reply poke;
- `tool_choice: none` on the last step with tools still declared (cache
  stability);
- prompt-token calibration (`ObserveUsage`);
- async tools and late results (`toolspec.ToolCall.Notify`);
- CLI providers: one call, no loop, tools reached over the host's MCP server.

**Sub-agents.** The SDK provides the mechanism: `Runner.Spawn(parent, task, opts)`
runs a child turn with a depth counter and a hard `MaxDepth`. Which agents
may be targeted, clones, worker models, temp-agent persistence and TTLs stay
in ClawEh (`agentreg`, `tools/agents`). `agentreg` itself is a later
candidate for the SDK (phase 7), not part of the first cut.

### 4.6 `spawnllm.Worker`

Once `agent` exists, `Worker` is a second, weaker loop. Options: delete it
(the module is unreleased) or reimplement it as a thin wrapper over the
engine with no session store. Recommendation: wrapper for one version, then
delete (D4). Maestro and MCPFusion usage must be checked first; I could not
see those repositories from this session.

### 4.7 Cancellation

```go
run := r.Run(ctx, sessionID, msg, opts...)   // *runner.Run
run.ID()                                     // turn id (ClawEh's turn_id)
run.Cancel(cause error)                      // idempotent, safe from any goroutine
run.Done() <-chan struct{}
r.Cancel(sessionID, cause) (running bool, dropped int)  // by session: running turn + queue
r.CancelAll(cause)                           // shutdown
```

Defined behaviour, each with a test:

1. The in-flight model call is aborted (HTTP request cancelled, CLI process
   group killed).
2. Running tools get a cancelled context; the engine waits a bounded grace
   period (`Limits.CancelGrace`) and then abandons them, recording a
   synthetic "cancelled" tool result so the stored transcript never has a
   tool call without a result (ctxengine already tolerates orphans; this
   avoids creating them).
3. Queued messages for the session are dropped and reported (today's
   `skipCount`).
4. Descendants: child turns started with `Spawn` in *wait* mode are
   cancelled with the parent. Background (callback) tasks are **not**
   cancelled by default, matching what a user expects from "stop this
   reply"; `CancelOptions{Background: true}` stops them too. (Confirm against
   current behaviour in phase 5 before fixing the default.)
5. Exactly one `TurnFinished{Outcome: cancelled}` event; `AfterTurn` runs
   with a context that is not cancelled, so hosts can persist and reply.
6. The cause distinguishes user cancel, shutdown, deadline and superseded;
   ClawEh's `errCancelledByUser` / `errShuttingDown` map onto them, and a
   shutdown-cancelled turn still stays pending for restart recovery.

ClawEh: `/cancel` becomes a call to `r.Cancel(sessionKey, …)` with identical
replies. New cancel surfaces (a WebUI stop button, device `chat.abort`, an
API endpoint) become possible but are **separate, later changes** that need
their own approval, changelog entries and protocol review.

## 5. What stays in ClawEh

Everything that is product, not engine: `bus`, `channels`, `routing`,
`commands` (incl. `/model`, `/tools`, `/cancel` parsing), the turn-reply
contract (`runTurn`, `reply_required`, `Outcome`), asks/whispers/turn slots,
human agents, forum, classifiers, task models, character cards, cron, voice
and media, the MCP host and session tokens, audit, alerts, restart recovery,
retention, message tokens, `ContextBuilder` (workspace prompt files, skills,
allowed folders), `cogmemhost`, the config `Store`, permissions, the WebUI.

`AgentLoop` remains as ClawEh's host object. It shrinks to: route inbound →
resolve the `AgentInstance` → call `runner.Run` with ClawEh's callbacks →
publish the reply. `AgentInstance` holds an `*agent.Agent` built from config
by `agentBuilder`.

## 6. Phases

Each phase lands on its own, with `make test` green in both repositories and
no change to `tests/capabilities/testdata/effective.golden`. Phases 0–4 are
moves with aliases left behind; behaviour risk is concentrated in phase 5.

**Phase 0 — ClawEh adopts `go.bison9.com/agent`.**
Swap the four `PivotLLM/*` requirements for the one module; update the alias
shims (`global`, `providers/aliases.go`), `cycle_guard_test.go`, and every
`Chat`/`Transcribe`/`Synthesize`/`Generate` call for the `accounting.Identity`
parameter. Wire `accounting` (sink location, permissions area, backup
inclusion, Check Up row) or leave it logging-only. Blocked on D2. Needs a
tagged SDK version (maintainer cuts it).
*Exit:* ClawEh builds on the module with no behaviour change;
`CLAUDE.md` "Shared modules" paragraph rewritten.

**Phase 1 — `modelspec`.**
Create the package; move the types; ClawEh `config` aliases them. Add a
round-trip test: ClawEh's fixture configs marshal byte-identically before and
after. Move `resolveRunLimits`' generic half.
*Exit:* `config` imports modelspec; no JSON diff; unknown-key warnings
unchanged.

**Phase 2 — `dispatch`.**
Move factory, client cache, fallback, cooldown with the Observer/BaseEnv
seams. ClawEh `providers/` becomes aliases plus `HumanProvider`, the alert
observer and `CreateProvider(cfg)`.
*Exit:* the CLI command lines and environments in the capabilities golden
file are identical; cooldown alerts fire with the same titles and keys
(`ALERTS.md` unchanged).

**Phase 3 — `toolkit`.**
Move the tool core; ClawEh `tools` keeps host types and aliases. Move
`untrusted`. `mcpserver` keeps working through the aliases.
*Exit:* `tests/test_mcpserver.sh` and the tool-count checks pass untouched.

**Phase 4 — `discovery`.**
Extract index, search, meta-tools and deferral out of `ToolRegistry`,
`search_tool.go`, `utils/bm25.go` and `sentToolDefs`. ClawEh's
`registerProviderTools` calls `discovery.Wrap` where it calls
`RegisterSuiteHidden*` now.
*Exit:* `tools/discovery_*_test.go` pass against the new package; identical
tool lists with discovery on and off.

**Phase 5 — the engine (`agent`, `runner`).** The large one; done inside
ClawEh first, then moved.

- 5a. In ClawEh, introduce the callback struct and event type and make
  `llmTurn` call them, with ClawEh's current behaviour as the callback
  implementations. No package moves. This is where the 38 imports are cut:
  each ClawEh dependency of `llm_iteration.go`, `llm_dispatch.go`,
  `tool_exec.go`, `loop_turn.go` becomes a hook or an interface
  (`Memory`, `SessionStore`, `MediaResolver`).
- 5b. Introduce the run handle and `Cancel` (4.7) in ClawEh; reimplement
  `/cancel` on it. Establish and test descendant behaviour.
- 5c. Move the now host-free engine files to `agent`/`runner` in the SDK;
  `AgentLoop` calls them.
- 5d. A minimal example host in the SDK (`examples/chat`): config file, two
  tools, stdin/stdout. It is the acceptance test for the goal in section 1
  and must stay under ~100 lines.

*Exit:* full `make test`, the MCP/classifier/task-model/coding-harness
integration scripts, **and the WebUI browser suite** (`make check-webui`,
this touches startup and every turn) on a dev instance; a real-device check
with the R1 for streaming order (`agent` events before `chat` final).

**Phase 6 — retire `spawnllm.Worker`** per D4; update Maestro/MCPFusion if
they use it.

**Phase 7 (optional, later) — `agentreg` into the SDK** as a generic registry
of built agents with temporary instances, if a second host needs it.

Rough size: phases 0–4 are each days of mostly mechanical work; phase 5 is
the bulk (the engine files are ~4,500 lines and the seam work touches most of
`agent/`). I have not estimated calendar time.

## 7. Testing

- Every moved package brings its tests; ClawEh keeps the tests of its
  callback implementations.
- New in the SDK: a scripted fake provider (ClawEh has several ad hoc ones
  to consolidate) driving table tests for fallback order, cooldown skip,
  both retry kinds, repeated-batch stop, discovery reveal, and each of the
  six cancel rules in 4.7 using the SDK's fake clock.
- Guards: `imports_test.go` per package; in ClawEh, extend
  `providers/cycle_guard_test.go` to assert no SDK package imports ClawEh.
- The capabilities golden file is the regression oracle for phases 1–4;
  the integration scripts and browser suite for phase 5.

## 8. ClawEh documentation and changelog

- `CLAUDE.md`: "Shared modules", "Providers", the tool-provider and discovery
  passages, and the clocks paragraph change as packages move; update in the
  same commits.
- `docs/tool-providers.md`: new import paths.
- `CHANGELOG.md`: none of phases 0–5 should be operator-visible. If
  accounting output is enabled in phase 0, that is an `Added` entry.
- SDK: README per package, `CHANGELOG.md` entries under 0.2.0 (or the next
  version if 0.2.0 is tagged for phase 0).

## 9. Risks

- **Phase 5 is a rewrite of the hottest path under a "no behaviour change"
  rule.** Mitigation is 5a: restructure in place, behind the existing test
  suite, before anything moves.
- **Callback overhead and ordering.** Several current behaviours depend on
  order (redaction before storage, audit before execution, images saved
  before the assistant message is stored). Hooks run in registration order
  and ClawEh registers them in one place, with a test that pins the order.
- **Alias sprawl.** Three layers of aliases (`global`, `providers`, `tools`,
  `config`) hide where types live. Plan a cleanup pass after phase 5 that
  replaces aliases with direct imports package by package.
- **Two repositories moving together.** Until the SDK is tagged, ClawEh
  needs a `replace` or pseudo-versions during development; `main` must never
  carry a `replace`.
- **Reload.** ClawEh swaps config, providers and agents live. `dispatch.Clients.Flush`
  and agent rebuilds must keep today's guarantee that a replaced provider is
  closed only after its in-flight calls end (`providerCalls`).
- **CLI providers bypass the engine's tool loop.** Callbacks around tools
  fire only for tools executed in-process; for CLI models they fire on the
  MCP host path. The SDK must document this, and ClawEh's MCP host should
  invoke the same `BeforeTool`/`AfterTool` chain so audit and redaction stay
  uniform.

## 10. Decisions needed

- **D1. Names.** `modelspec`, `dispatch`, `toolkit`, `discovery`, `agent`,
  `runner`. Note the module is already called `agent`, so the package would
  be `go.bison9.com/agent/agent`; alternatives are `core` or putting the
  engine at the module root.
- **D2. Accounting identity length.** 16 characters cannot hold ClawEh agent
  ids (≤64) or temporary-agent UUIDs. Options: raise or remove the limit in
  `accounting`; or have ClawEh pass a shortened label (loses the join to
  audit rows). Recommendation: raise the limit to 64 in the SDK, since ids
  are already validated by the host. Blocks phase 0.
- **D3. `CLAW_COOLDOWN_*` env tags** on a struct that moves to the SDK: keep
  the tags there, or keep a ClawEh-side struct.
- **D4. `spawnllm.Worker`:** wrapper then delete, or delete now. Depends on
  Maestro/MCPFusion usage.
- **D5. Did "the package that defines our tool provider interface" mean
  ClawEh's `tools` core** (my reading, since `toolspec` is already in the
  module)? If it meant something else, section 4.3 changes.
- **D6. Session storage in the SDK.** ctxengine's SQLite store as the only
  implementation behind an interface (recommended), or also an in-memory one
  for small hosts and tests.
- **D7. Default for background tasks on cancel** (4.7 rule 4).
- **D8. Branching.** The work spans several ClawEh versions. Suggest phase 0
  on the current `dev/<version>` branch and each later phase on the dev
  branch of the version it ships in, never a long-lived SDK branch.
- **D9. Is memory (`cogmem`) part of the first cut** as an optional
  `agent.Memory`, or added after the engine is stable? The loop's
  observe/recall calls are few, so including the interface from the start is
  cheap; recommended.
