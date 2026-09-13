# Modular architecture: context engine, memory, agent

Status: **proposal; first steps under way.** Written 2026-09-13 from a survey
of the current code and revised the same day after discussion settled on three
components rather than four. Decisions marked *taken* were agreed in
discussion; the open questions are collected in section 13. The in-place
cleanups of section 12 and the cogmem extraction are being done on the branch
`feature/modular-cleanups-cogmem-extract`.

The aim is to carve ClawEh into components with contracts narrow enough that a
component can be swapped, tested to destruction on its own, and reused from a
different application. The context engine is the first target; the agent loop,
memory and the tool contract follow from the same cut.

---

## 1. What the survey found

The seams are better than the file sizes suggest, and the debt is concentrated
in a few joins.

| Area | Finding | Where |
|---|---|---|
| Context | `llmcontext.ContextManager` already exists and nothing outside `agent/` constructs one. It has 20 methods, three more setters reached by type assertion for cogmem, and two overlapping emergency triggers. | `llmcontext/interface.go:13`, `llmcontext/manager.go:1005-1022` |
| Prompt | System prompt assembly is split: identity, bootstrap files, skills, `MEMORY.md` and the date are built in `agent/context.go`; the session token block and the cogmem blocks are appended inside the engine's `Build`. | `agent/context.go:199`, `llmcontext/manager.go:1090-1142` |
| Cogmem | `cogmem/store`, `cogmem`, `cogmem/portable` and `cogmem/consolidate` import nothing from the loop, session or providers. Glue is one file plus one gateway adapter. The operating instructions are hard-coded in the identity prompt and emitted even when cogmem is off. | `agent/memory_wiring.go`, `internal/gateway/cogmem.go`, `agent/context.go:181` |
| Transcript | Two message stores per session: a JSONL live window and a SQLite archive with FTS, summary checkpoints and a consolidated flag. Session tools open the archive by file path. | `memory/jsonl.go`, `memory/archive.go`, `tools/session/global_provider.go:43` |
| Loop | One struct owns bus consumption, routing, mentions, commands, session tokens, dispatch and fallback, tool execution with media and vision fan-out, streaming, eviction notices, recovery and heartbeats. spawnllm's `Worker` is not used. Three tool loops exist. | `agent/loop.go` (4,399 lines), `tools/toolloop.go`, spawnllm `Run` |
| Tools | `toolspec.ToolDefinition` plus `ToolHandler` is a clean portable contract shared with MCPFusion. ClawEh wraps it into the legacy `tools.Tool` interface and a `ToolDeps` struct carrying closures. `ToolCall.AgentID` is never populated; the session key travels by context value. | `tools/namespaced.go:77`, `tools/provider.go:296` |
| Agents | No tool can create or edit an agent. Only the WebUI's whole-config PATCH does. An agent cannot read or edit its own prompt files. Default summarization and consolidation prompts are code-owned with append-only markdown overrides. | `web/backend/api/config.go`, `templates/AGENTS.md:98-103`, `llmcontext/summary.go:578`, `cogmem/consolidate/prompt.go:70` |

Hard-won behaviour that must survive the refactor, and be encoded as tests
before it starts:

- Prompt layers ordered by volatility so the cached prefix stays byte-identical
  (measured cost when wrong: 48 to 61 percent of full-price input tokens).
- Compaction never archives past the most recent user message and never
  empties the window.
- Summaries must cite seq evidence; refusals are remembered per model.
- Eviction: only a successful write supersedes a read; arguments are evictable.
- Device gateway event order: `agent` stream before `chat` final.

---

## 2. Goals and non-goals

Goals, in priority order:

- **P1** A context engine that can be replaced behind a contract, with the
  built-in engine as the reference implementation and the fallback.
- **P2a** An agent that other applications can instantiate: loop, engine,
  memory, tools, model policy, with no dependency on the bus, channels, routing
  or the WebUI.
- **P2b** Every component testable in isolation with Go tests and, for the
  engine, an external simulator that can run with or without a real model.
- **P3** A bootstrap path and a trusted administrative agent that can create
  and maintain other agents.

Non-goals:

- Compatibility with OpenClaw's plugin API. Ideas are borrowed; the contract
  is ours.
- Hot-loading new Go code at runtime. Pluggability is by configuration over
  compiled-in implementations, with out-of-process implementations reachable
  through adapters.
- A message bus on the turn's critical path (section 8).

---

## 3. Borrowed from OpenClaw, and left behind

Taken:

- Four lifecycle points: ingest, assemble, compact, after-turn.
- The engine returns its own token estimate and the host trusts it.
- A system prompt addition so an engine can describe itself.
- Failure isolation: an engine that fails is bypassed for the turn and the
  built-in engine answers.
- Idempotent turn commit keyed by a client-supplied key, so a recovery replay
  cannot double-append.
- Sub-agent hooks: fork versus isolated context at spawn.

Left:

- Plugin slots, `acceptedHostParams`, transcript-semantics declarations and
  the rest of the plugin negotiation. Go interfaces and compile-time
  registration are enough.
- `ownsCompaction`. Every engine owns its compaction; the built-in engine is
  the fallback, not a co-pilot.
- The legacy engine's "ingest is a no-op, the session manager persists". Our
  engine owns the transcript (section 4): `Ingest` assigns the seq and archives
  the message, and there is no session manager beside it.

---

## 4. Component model

Three components plus the leaf modules that already exist.

```
   spawnllm (Message, providers)     toolspec (ToolDefinition, ToolCall)     prompt (Layer, Injection)
              ▲                              ▲                                      ▲
              ├──────────────────────────────┼──────────────────────────────────────┤
           context                        memory                                     │
     archive + window + summaries    inbox + recall + consolidation                  │
     session tools                   memory tools                                    │
              ▲                              ▲                                      │
              └────────────── agent ─────────┴──────────────────────────────────────┘
                     runner, model policy, tool set, prompt composer
                               ▲
                             ClawEh        bus, channels, routing, config, WebUI, MCP server
```

Dependency rules, enforced by a cycle-guard test like `providers/cycle_guard_test.go`:

- **4.1** `prompt` imports only stdlib.
- **4.2** `context` and `memory` import `prompt`, spawnllm, toolspec and
  stdlib. Neither imports the other, and neither imports `agent`.
- **4.3** `agent` imports all of the above. It is the only place the three are
  wired together.
- **4.4** ClawEh imports `agent`. Nothing under the line imports ClawEh
  packages: not `config`, not `logger` directly (a logger interface is
  injected), not `bus`, not `routing`.

The one-sentence rule: **every component is a self-contained observer of the
turn; the agent hands each one a copy of every message and gathers what they
return, and context and memory never see each other.**

### 4.1 The context engine owns what was said (*taken*)

The engine owns the whole record of the conversation: it assigns the seq on
`Ingest`, archives the message, maintains the live window and the rolling
summary, writes summary checkpoints that cite those seqs, and ships the tools
that read all of it (`session_messages`, `session_search`,
`session_summary_*`, `session_compact`, `session_info`, `session_clear`).
One component, one story, and the seq is never invented anywhere else.

The condition that makes this safe: the archive is a **package inside the
engine module with its own interface** (`Append`, `Range`, `Bounds`, `Search`,
`Checkpoints`, `Prune`, `ListSessions`), not private state of the default
engine. A replacement engine changes the assembly and compaction strategy and
composes the same archive package, so history survives the swap and the
session tools come along. A failed plug-in engine is replaced for the turn by
the built-in one, which opens the same archive and rebuilds a window.

Earlier drafts made the transcript a fourth, host-owned component so that
memory and the engine could both read it. That is unnecessary once memory
keeps its own copy (4.2): nothing but the engine needs to read the archive
during a turn, and the tools that read it afterwards ship with the archive
package.

### 4.2 Memory owns what was learned, and its own inbox (*taken*)

Zero or more providers per agent, ordered. Each receives a copy of every
stored message (`Observe`) and keeps it in its own inbox until its background
consolidation has covered it, then drops it. So a memory provider depends on
nothing else for its input: no archive reads, no retention holds, no
"consolidated" flag on somebody else's table. Cogmem is the first
implementation and already works this way. A retrieval-only source such as a
corporate RAG can be an MCP search tool plus a thin recall adapter.

Each provider publishes three things: a runner interface (`Open`, `Observe`,
`Recall`, `AfterTurn`, `Close`), a **typed Go API** for the GUI, the CLI and
tests, and a **tool list derived from that API** for the model. The typed API
is the contract; the tools are a thin layer inside the module so the
descriptions, the argument names and the operating instructions in
`Guidance()` are versioned with the behaviour they describe. The WebUI never
drives a component through tool calls: it needs typed structs, paging,
structured errors and operations that must never be tools (forget versus
retire, import with replace, purge).

### 4.3 The agent owns the turn

Identity, workspace, prompt composer, engine factory, memory providers, tool
set and model policy. `Runner.Turn` is the loop. It knows nothing of channels.

### 4.4 Tools

`toolspec` end to end. Each component returns the tools it owns, built over its
typed API; the agent collects them into one tool set; ClawEh's own tools
(files, web, shell, messaging, cron, agent-admin) are host tools and may import
`agent`. Per-agent allow lists, suite gating, discovery hiding and the MCP
server exposing the same definitions stay with the host.

---

## 5. Contracts

Sketches, not final signatures. Types named `Message` are spawnllm's via the
`providers` alias.

### 5.1 `prompt`

```go
type Volatility int   // Static, Daily, Session, Turn
type Placement  int   // SystemStable, SystemSession, CurrentUser

// Layer is a block of the system prompt. The host renders layers in
// volatility order so the cacheable prefix stays byte-identical.
type Layer struct {
    Name       string
    Volatility Volatility
    Text       string
}

// Injection is a memory block with a declared placement. Priority orders
// trimming when the budget is short; lower is dropped first.
type Injection struct {
    Source    string   // provider id
    Name      string
    Placement Placement
    Priority  int
    Text      string
}
```

Placement encodes the caching decision that was expensive to learn: stable
memory rides in the system prefix; routed memory is folded into the current
user message so it never invalidates the prefix.

### 5.2 `context/archive`

A package inside the engine module. Engines compose it; nothing else opens it.

```go
type Entry struct {
    Seq       int64
    CreatedAt time.Time
    Message   Message
}

type Checkpoint struct {
    Model        string
    Profile      string      // guidance fingerprint
    SourceStart  int64
    SourceEnd    int64
    CoveredStart int64
    CoveredEnd   int64
    Body         []byte      // JSON summary
    CreatedAt    time.Time
}

type Archive interface {
    Append(ctx context.Context, msg Message, at time.Time) (int64, error)   // assigns the seq
    Range(ctx context.Context, from, to int64, limit int) ([]Entry, error)
    Bounds(ctx context.Context) (min, max int64, err error)
    Search(ctx context.Context, query string, limit int) ([]Entry, error)
    AppendCheckpoint(ctx context.Context, cp Checkpoint) error
    ListCheckpoints(ctx context.Context, limit int) ([]Checkpoint, error)
    Prune(ctx context.Context, p PrunePolicy) (removed int, err error)
    Close() error
}

func Open(path string) (Archive, error)
func InMemory() Archive
func ListSessions(dir string) ([]string, error)
```

The typed API the WebUI session view and the device gateway's `chat.history`
call, instead of reading JSONL files by path as they do today.

### 5.3 `context`

```go
type Info struct{ ID, Name, Version string }

type Deps struct {
    Archive    archive.Archive   // nil or archive.InMemory() when embedded
    Summarizer Summarizer
    Clock      Clock
    Guidance   string            // per-agent compaction guidance, today COMPRESSION.md
    Settings   Settings          // window, thresholds, retention; engine-specific extras as a map
    Log        Logger
}

type Engine interface {
    Info() Info
    Open(ctx context.Context, session string, deps Deps) (Session, error)
    Tools(resolve SessionResolver) []toolspec.ToolDefinition   // session_* : messages, search, summaries, compact, info, clear
}

type SessionResolver func(session string) (Session, error)

type Session interface {
    // Ingest archives a message and records it in the window. It returns the
    // transcript seq, the number everything else cites: summaries, session
    // tools, memory evidence. The seq is minted here and nowhere else.
    Ingest(ctx context.Context, msg Message) (int64, error)

    // Assemble is called before every model call. The engine compacts or
    // evicts internally if it must; Compacted and Notices report that.
    Assemble(ctx context.Context, req AssembleRequest) (Assembly, error)

    // Compact runs an on-demand pass (the /compact command, overflow recovery).
    Compact(ctx context.Context, req CompactRequest) (CompactReport, error)

    AfterTurn(ctx context.Context, outcome TurnOutcome) error
    Reset(ctx context.Context) error
    Stats() Stats
    Close(ctx context.Context) error
}

type AssembleRequest struct {
    Budget     int                 // tokens for the whole request
    Reserved   int                 // tool schemas and completion reserve, host-estimated
    Layers     []prompt.Layer      // host-built system prompt
    Injections []prompt.Injection  // memory output, gathered by the host
}

type Assembly struct {
    Messages        []Message
    EstimatedTokens int
    Addition        prompt.Layer   // engine self-description, may be empty
    Notices         []string       // one-line, user-visible
    Compacted       bool
}

type Summarizer interface {
    Complete(ctx context.Context, msgs []Message, opts CompleteOptions) (Reply, error)
}
type Reply struct{ Content, FinishReason, Model string }
```

Design notes:

- One `Assemble` per model call replaces today's `Build`, `PreDispatchCheck`,
  `CheckAndCompress` and `SweepEvictions`. The engine decides when to compact;
  the host only supplies the budget. (Done in place on the branch: see
  section 12.)
- `Ingest` failing fails the turn. There is no reply without a record.
- The host's `Summarizer` walks the model chain, applies cooldowns and reports
  `Model` on the reply, so the engine can remember refusals per model without
  knowing the chain exists.
- The engine never reads a workspace file. Guidance arrives as a string.
- `Clock` is injected so age triggers and retention are testable.

### 5.4 `memory`

```go
type Provider interface {
    Info() Info
    Open(ctx context.Context, agent, session string, deps Deps) (Session, error)
    Tools() []toolspec.ToolDefinition     // handlers resolve the session from ToolCall.Session
    API() *API                            // typed surface for the GUI, CLI and tests
}

type Deps struct {
    Model     Summarizer        // same shape as context.Summarizer; consolidation reuses it
    Clock     Clock
    Guidance  string            // per-agent instructions, today COGMEM.md
    Settings  Settings
    Files     AttachmentLoader  // host-owned file access for attachments
    Log       Logger
}

type Session interface {
    // Observe hands the provider a copy of a message the engine stored under
    // seq. It lands in the provider's own inbox until consolidation covers it.
    Observe(ctx context.Context, seq int64, msg Message) error
    Recall(ctx context.Context, req RecallRequest) (RecallResult, error)
    AfterTurn(ctx context.Context, outcome TurnOutcome) error
    Guidance() prompt.Layer                  // self-description for the system prompt
    Close(ctx context.Context) error
}

type RecallRequest struct {
    Text        string     // latest user text
    RecentTools []string
    Budget      int        // characters this provider may return
}
type RecallResult struct{ Injections []prompt.Injection }
```

Consolidation stays inside the provider: it drains its inbox by watermark,
runs on its own timers, and is nudged by `AfterTurn`. Nothing pushes into it,
and it reads nothing outside its own store. `Tools()` lives on the provider,
not the session, because handlers resolve the session from the call; `API()`
is what `Tools()` is built on, so the two cannot disagree.

### 5.5 `tools`

```go
type ToolSet interface {
    Definitions() []toolspec.ToolDefinition
    Call(ctx context.Context, name string, args map[string]any, cc CallContext) (*toolspec.Result, error)
}

type CallContext struct {
    AgentID, Session, Channel, ChatID string
    Notify func(*toolspec.Result)      // async delivery, nil when the host has none
}
```

`Call` builds the `toolspec.ToolCall` with every field populated, every time.
Discovery (hidden tools, TTL promotion, `search_tools`) is a `ToolSet`
decorator, not a property of the registry. The MCP server and the MCP client
adapter both speak `ToolSet`.

### 5.6 `agent`

```go
type Agent struct {
    ID, Workspace string
    Prompt   prompt.Composer         // identity, bootstrap files, skills, MEMORY.md, date, runtime
    Engine   context.Engine
    Memory   []memory.Provider       // ordered, may be empty
    Tools    ToolSet
    Models   ModelPolicy             // candidates, fallback, cooldown, per-model options
    History  history.Store
}

type TurnRequest struct {
    Session        string
    Input          Message
    Channel, ChatID string
    IdempotencyKey string           // recovery replays reuse it
    Retry          bool
}

type Sink interface {
    OnDelta(text string)
    OnToolActivity(line string)
    OnNotice(line string)
    OnFinal(reply Message, stats TurnStats)
}

type Runner interface {
    Turn(ctx context.Context, req TurnRequest, sink Sink) (TurnResult, error)
}
```

The runner owns per-session engine and memory instances with the refcount and
TTL eviction that `agent/eviction.go` does today. The `Sink` is exactly the
event stream the device gateway needs: deltas, tool activity, notices, final.

---

## 6. The turn

- **6.1** `seq := engine.Ingest(user)`. The engine archives the message and
  returns its seq; a failure fails the turn.
- **6.2** For each memory provider: `Observe(seq, user)`. Optional; a failure
  is logged and the turn continues.
- **6.3** For each memory provider concurrently, with a per-provider timeout:
  `Recall(text, recentTools, budget)`. A provider that errors or times out
  contributes nothing this turn and is logged.
- **6.4** Compose layers: identity, bootstrap files, skills, `MEMORY.md`,
  each memory provider's `Guidance()`, the engine's addition from the last
  assembly, the date, runtime, session token. Sort by volatility.
- **6.5** `asm := engine.Assemble(budget, reserved, layers, injections)`.
  Surface `asm.Notices` through the sink.
- **6.6** Call the model through `ModelPolicy`. Stream deltas to the sink.
- **6.7** For each tool call: `Tools.Call`, ingest the assistant message and
  each result (each returning a seq, each observed by memory), fan out media
  and images as today, then go to 6.5. Iteration and timeout limits as now.
- **6.8** Ingest and observe the final reply, then `engine.AfterTurn` and each
  `memory.AfterTurn`.

If `Assemble` fails, the runner opens the built-in engine over the same
archive for this turn, logs the failure with the engine id, and marks the
plug-in engine quarantined for the session.

---

## 7. Configuration and pluggability

Implementations register by name in Go, the way tool providers do today.
Config selects and parameterises them per agent, with defaults at
`agents.defaults`:

```yaml
agents:
  defaults:
    context_engine:
      type: default
      settings: { context_window: 128000, compression: { trigger: { normal_percent: 50 } } }
    memory:
      - type: cogmem
  list:
    - id: alice
      memory:
        - type: cogmem
        - type: mcp-recall          # thin adapter: calls an MCP search tool at recall time
          server: corp-docs
          tool: search
          top_k: 5
          placement: current_user
    - id: bob
      memory: []                    # no learned memory at all
```

Removing cogmem is an empty list. Adding retrieval is a stanza. A config
reload rebuilds the agent's provider list through the existing reload path.
An out-of-process engine or provider is an adapter behind the same interface;
the transport is not the contract.

Existing keys keep working: `cogmem: false` maps to an empty memory list,
`compression` and `context_eviction` map into the default engine's settings,
`summarization_models` feeds the host's `Summarizer`. These mappings are
compatibility decisions to confirm before implementation (section 13).

---

## 8. Commands are calls, events are the bus

A message bus was considered for the whole turn and rejected for the critical
path. The turn is a synchronous pipeline: each step needs the previous result,
a timeout, a budget, and an error decision. Over a bus that becomes request
and response with correlation ids and "did everyone answer", with no
compile-time types and no stack traces. It would also make the engine
untestable by calling it.

Events are right for observers. The engine publishes an append event on the
host bus after 6.8. Consumers: the WebUI live view, audit, a future indexer.
Memory providers do not need it; they already received the message in
`Observe` and drain their own inbox.

---

## 9. Failure isolation

| Failure | Behaviour |
|---|---|
| Plug-in engine errors in `Open` or `Assemble` | Built-in engine answers this turn over the same archive; engine quarantined for the session; error logged with engine id and operation. |
| Summarizer refuses or fails | Engine records per-model refusal; host applies cooldown; compaction reports partial as today; circuit breaker after repeated failures. |
| Memory provider errors in `Observe` or times out in `Recall` | Provider skipped for that message or turn; the reply is not blocked. |
| Memory provider errors in `AfterTurn` | Logged; never affects the reply. |
| Tool errors | Unchanged: error result to the model, typed as tool error so eviction never treats it as a successful write. |
| `engine.Ingest` fails | Turn fails. There is no reply without a record. |

---

## 10. Testing

### 10.1 Engine conformance suite

A package `context/enginetest` with `Run(t, factory)` that any engine must
pass, in the spirit of `nettest`. Invariants:

- The latest user message is always in the assembled messages.
- No orphan tool results, no leading tool messages, no assistant message
  without its tool group.
- `EstimatedTokens` never exceeds `Budget - Reserved` after a successful
  assemble.
- `Ingest` with a repeated seq is a no-op.
- `Compact` then `Assemble` lands at or under the target.
- `Reset` leaves the archive intact and the view empty.
- Crash between `Ingest` and `AfterTurn`, reopen, assemble: the window is
  consistent with history.
- Layers render in volatility order and the static prefix is byte-identical
  across two consecutive assemblies with no file changes.

### 10.2 Scripted summarizer

A `Summarizer` fake driven by a script: valid JSON, malformed JSON, JSON in
prose, refusal finish reason, timeout, empty content, gain below threshold,
each per call. Every branch of the compaction loop is reachable
deterministically. A `CLAW_TEST_REAL_LLM` mode swaps in a real chain for soak
runs; the same tests run, the assertions relax to invariants.

### 10.3 Simulator

An external harness under `tests/ctxsim`, driven by a scenario file:
synthetic or recorded transcripts, tool-heavy sessions, cron spam, media,
long idle gaps under a fake clock, and fault injection (archive write
failure, summarizer failure, context cancel during compaction). After every
step it checks the conformance invariants and records tokens per turn,
compaction count, gain and duration. Output is a report the user can read and
a JSON file a pipeline can diff. It runs against any registered engine.

### 10.4 Golden prompts as the refactor oracle

Before any code moves, record assembled prompts from the current engine for a
set of fixed sessions. The adapted engine must produce byte-identical output
for the same inputs. This is the parity gate for phase A in section 12.

### 10.5 Runner and providers

The runner is tested with a scripted provider that returns tool calls, empty
replies, the `!none` sentinel and streaming deltas, checking tool execution,
fan-out, ordering of sink events and iteration limits. Memory providers get
the same treatment: `Recall` with a fixed store, `AfterTurn` with a fake
clock, consolidation with a scripted model.

---

## 11. Bootstrap and a chief of staff

### 11.1 Self-describing subsystems

Each engine and memory provider contributes its own operating instructions as
a layer. The cogmem paragraph leaves `agent/context.go` and appears only when
cogmem is active. A RAG provider says how to ask for documents. The engine
explains that summaries are approximate and which tools reach the archive.
The user never has to teach an agent how to use a subsystem the operator
plugged in.

### 11.2 Agent-admin tool suite

A host tool suite, default off, enabled by `admin: true` on an agent:

- `agent_list`, `agent_get`, `agent_validate`
- `agent_create`, `agent_update` (models, tools, memory, bindings, enabled)
- `agent_prompt_get`, `agent_prompt_set` for `AGENTS.md`, `SOUL.md`,
  `IDENTITY.md`, `USER.md`, `MEMORY.md`, `COMPRESSION.md`, `COGMEM.md`
- `agent_snapshot_list`, `agent_snapshot_restore`

It writes through the same save-and-reload path the WebUI uses, and writes
workspace files through an explicit capability rather than by loosening the
`files/` sandbox. Guardrails mirror the spawn rule: an admin agent cannot
grant a tool, model or suite it does not itself have, cannot set or clear
`admin`, and every change is snapshotted with a diff before it applies.

### 11.3 Alice, chief of staff

A shipped agent profile with the admin suite and an `AGENTS.md` that is a
playbook: interview the user and write `USER.md`, propose agents for the work
described, draft each one's `SOUL.md`, `AGENTS.md` and `IDENTITY.md`, choose
memory providers and domains, and review after use. The growing body of
knowledge on how to instruct agents ships as **skills**, because skills are
discoverable, versioned independently of the binary, and overridable per
workspace: `agent-design`, `memory-usage`, `prompt-review`, `channel-guidance`.

### 11.4 First run

`claw init` (or the WebUI on first launch) creates Alice as the default agent
and sets a first-run flag. While the flag is set an `ONBOARDING.md` layer is
loaded that tells Alice to run the interview. Alice clears the flag through
an admin tool when the user says the setup is done.

### 11.5 Default prompts and overrides

The engine and each provider carry their default prompts in code, as now.
Per-agent guidance is passed in as a string the host reads from the
workspace, so the pattern "defaults are reviewed, files only add" survives
and the engine no longer knows the file exists.

---

## 12. Mapping from today's code

| Today | Becomes |
|---|---|
| `memory/archive.go` | `context/archive` |
| `memory/jsonl.go`, `session/` | engine-private window state; folds into the archive as a seq range plus summary |
| `llmcontext/*` | built-in `context` engine |
| `agent/context.go` `ContextBuilder` | `prompt.Composer` in `agent` |
| `agent/context_manager.go` chain resolution | host `Summarizer` in `agent` model policy |
| `agent/memory_wiring.go`, `internal/gateway/cogmem.go` | cogmem `memory.Provider` (module `github.com/PivotLLM/cogmem`) |
| `cogmem/*`, `tools/cogmem` | the cogmem module: store, composer, consolidation, portable, tools |
| `cogmem/attachfile` | stays in ClawEh as the `AttachmentLoader` implementation |
| `tools/session` | `context.Engine.Tools()` over the archive API |
| `agent/loop.go` `runAgentLoop`, `runLLMIteration` | `agent.Runner` |
| `agent/loop.go` bus, routing, mentions, commands, session tokens | stay in ClawEh, call `Runner.Turn` |
| `agent/eviction.go` | session lifecycle inside `Runner` |
| `tools/registry.go`, `tools/namespaced.go`, `tools/toolloop.go` | one `ToolSet` with a discovery decorator; one tool loop |
| `providers/fallback.go`, `cooldown.go`, `dispatch.go` | `ModelPolicy` in `agent` |
| `channels/device`, `internal/gateway/device_query.go` | a `Sink` implementation |

### 12.1 In-place cleanups done first (branch `feature/modular-cleanups-cogmem-extract`)

Each is independently shippable and was gated by `make check` and the prompt
parity test:

- Cogmem keeps its own inbox: `consolidate.Observe` per message, the archive
  adapter, `MarkConsolidated` and `protect_unconsolidated` removed, a one-time
  backfill from the archive on first open after upgrade.
- The cogmem operating rule moved to `cogmem.Guidance()` and is emitted only
  for agents with cogmem on, byte-identical for those that have it.
- `llmcontext.ContextManager` has one `Assemble` per dispatch; `Build`,
  `PreDispatchCheck`, `CheckAndCompress`, `SweepEvictions`,
  `SetToolDefinitionTokens`, `RecordToolUse`, `SetMemoryBlocks` and
  `SetArchiveAppendHook` are gone from the interface. `Add*` return the seq.
- The loop holds a `memorySession` beside the context manager and is the only
  thing that touches both: Observe after every Add, Recall before every
  Assemble.
- One session-file naming rule (`memory.SanitizeSessionKey`, `memory.ArchivePath`)
  replaces five copies; the WebUI's copy was wrong for keys containing `/`.
- `ToolCall.AgentID` is populated on every call.
- `agent/loop.go` split by concern into `loop_inbound.go`, `loop_turn.go`,
  `loop_tools.go`, `loop_message_tokens.go`, `loop_transcribe.go`,
  `loop_session_state.go` and `loop_commands.go`, with no API change.

---

## 13. Sequencing and open questions

Phases, in order, each gated by `make check` plus the parity oracle. No
timelines.

- **A** Define `prompt`, `context` (with `archive`) and `memory` contracts
  and the conformance suite. Adapt the existing manager and cogmem behind them. Golden
  prompts must match byte for byte. The loop calls only the new contracts.
- **B** Extract `Runner` from `AgentLoop`. ClawEh's loop becomes bus,
  routing, commands and a `Sink`.
- **C** Unify tools on `ToolSet`; retire `tools.Tool`, `ToolDeps` closures
  and the extra tool loops.
- **D** Simulator and fault injection against the built-in engine.
- **E** Extract to a module the way spawnllm was, once the API has held
  through B and C.
- **F** Admin suite, Alice, skills, first run.

Decisions taken in discussion:

- Three components: the engine owns the transcript and the archive package;
  memory keeps its own inbox; the agent runs the turn.
- Cogmem is a memory provider, not part of the engine, and is extracted to its
  own module first because it is the least entangled.
- Interfaces and a provider registry on the turn path; events for observers.
- Components ship their own tools, built over a typed API the GUI also uses; a
  central tools package holds host tools only.
- The document-processing consumer is written against the module before the
  runner is extracted, so that consumer dictates the runner's API.

Open questions:

- **13.1** Must admin-agent changes wait for approval in the WebUI, or does a
  trusted agent act directly with snapshots for rollback?
- **13.2** Compatibility of existing keys (`cogmem: false`, `compression`,
  `context_eviction`, `summarization_models`): map them silently, or
  introduce the new `context_engine` and `memory` blocks with a documented
  migration? The project is released, so this is a **BREAKING** decision
  either way and needs a changelog entry.
- **13.3** Does recovery today double-append the replayed user message? The
  idempotent inbox append (`INSERT OR IGNORE` on seq) already tolerates it;
  the archive's `INSERT OR REPLACE` does too. Verify the JSONL window before
  relying on it.
- **13.4** Should sub-agent spawn use fork or isolated engine context by
  default? Today a sub-agent gets a cogmem snapshot and a fresh session.
