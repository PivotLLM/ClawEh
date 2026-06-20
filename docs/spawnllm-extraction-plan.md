# spawnllm extraction — plan & checklist

Working plan for pulling ClawEh's LLM-dispatch core into a standalone module,
`github.com/PivotLLM/spawnllm`. This is a planning/tracking doc; remove it when
the work lands.

## Goal

Extract the code that **calls an LLM and drives it to completion** — for CLI
providers, run the subprocess and return its result; for API providers, run the
LLM↔tool-call loop until the model is done — into a reusable, dependency-free
module that both ClawEh and Maestro can compile in (single binary).

Everything *policy* stays in ClawEh: which model to call, fallback, cooldown,
config, results handling, the agent pipeline (cogmem, channels, compaction,
async callbacks). spawnllm is the **single-provider dispatch primitive**, not a
replacement for ClawEh's agent loop.

## Module DAG (single binary, acyclic)

```
toolspec ◄── spawnllm ◄── claweh   (claweh maps config → ProviderSpec; owns dispatcher/fallback/cooldown)
                       ◄── maestro  (builds ProviderSpec from its file config; injects its own tools)
```

spawnllm depends only on `toolspec` + `logger` + stdlib/net-http. It never
imports ClawEh's `pkg/tools`, `pkg/config`, `pkg/auth`, or any concrete tool.

## Circular imports — the invariant (read this first)

The runtime call graph re-enters spawnllm:

```
agent loop → spawnllm.Run (API loop, ClawEh tools injected)
           → LLM emits a tool call: agent_spawn
           → spawn tool executes (ClawEh code) → dispatches a sub-agent
           → sub-agent runs → spawnllm.Run (again)
```

This is **not** an import cycle, and must never become one. The guarantee:

- **spawnllm imports only `toolspec`.** Tools — including the spawn tool — are
  *injected* as `toolspec.Tool` values (DI). spawnllm has zero compile-time
  knowledge of any concrete tool. ClawEh imports spawnllm; spawnllm never imports
  ClawEh. One-directional → acyclic.
- **No infinite recursion at runtime:** `agent_spawn` is `PrimaryOnly`, so a
  sub-agent's injected tool set excludes it. The inner spawnllm.Run has no spawn
  tool. (Existing guard; the extraction must preserve it.)

Invariant to enforce in review: **nothing under `spawnllm/` may import
`github.com/PivotLLM/ClawEh/...`.** Add a guard test (below).

## Dispatcher question (your suggestion)

Considered carving a dispatcher into a shared module. **Not worthwhile:** model
selection + fallback + cooldown are config/policy, and pulling them out would
drag ClawEh's config schema into a shared layer (the exact coupling we're
avoiding). ClawEh keeps a thin dispatch+fallback layer that maps `config` →
`spawnllm.ProviderSpec` and loops candidates around `spawnllm.Run`/client calls.
spawnllm stays deliberately single-provider.

## What moves into spawnllm

Clean (verified config/auth-free): the concrete clients + protocol DTOs +
provider interfaces.

- DTOs: `pkg/providers/protocoltypes/*` → spawnllm (Message, ToolDefinition,
  LLMResponse, ContentBlock, …).
- Interfaces: `LLMProvider`, `CLIProvider`, `StatefulProvider`, `ThinkingCapable`
  (`types.go`).
- API clients: `http_provider.go`, subpackages `anthropic`, `anthropic_messages`,
  `azure`, `openai_compat`, `openai_responses`, `common`.
- CLI clients: `claude_cli_provider.go`, `codex_cli_provider.go`,
  `gemini_cli_provider.go`, `cli_env.go`, `cli_options.go`,
  `codex_cli_credentials.go`.
- Support (verify each is config/auth-free at move time): `context.go`,
  `model_ref.go`, `toolcall_utils.go`, possibly `error_classifier.go`,
  `unconfigured.go`, `claude_provider.go`.

New in spawnllm:

- `ProviderSpec{ Kind, BaseURL, APIKey, CLIPath, Model, Opts }` (resolved values).
- `New(...Option)` → disposable worker. Options: `WithProvider(ProviderSpec)`,
  `WithTools([]toolspec.Tool)`, `WithHTTPClient(*http.Client)`,
  `WithMaxIterations(int)`, `WithProgress(func(...))`. `New` returns an error
  (validates CLI-xor-API + model set).
- `Run(ctx, messages) (Result, error)` — CLI: invoke + capture; API: run the
  tool loop over injected tools until done. Returns content/transcript/iterations.
- A **lean loop** written fresh over `[]toolspec.Tool` (convert toolspec params →
  provider ToolDefinition, call LLM, execute injected tools). NOT a move of
  `RunToolLoop` — that uses the registry/dispatcher/fallback and stays in ClawEh.

## What stays in ClawEh

- `dispatch.go`, `factory_provider.go`, `legacy_provider.go` — refactored to map
  `config.Config` → `spawnllm.ProviderSpec` / build clients.
- `fallback.go`, `cooldown.go` (+ `error_classifier.go` if fallback needs it).
- `pkg/tools` registry (MCP, namespacing, ACL) and `RunToolLoop`.
- The agent pipeline (`pkg/agent`), unchanged.
- `pkg/providers` becomes a thin **alias shim** re-exporting the moved
  types/clients under their current names, so ClawEh call sites compile
  unchanged (type aliases = identical types; `var X = spawnllm.X` for funcs).

ClawEh's spawn tool injects ClawEh's own tool package(s) into the spawnllm worker
it builds for a sub-agent (your point: ClawEh supplies the tools for the spawn).

## Scope guard (this phase)

MOVE clients/DTOs/interfaces + ADD the ProviderSpec worker API. Alias in ClawEh
so behavior is unchanged. Do **not** rewire ClawEh's `runAgentLoop` or change
sub-agent execution semantics to use the new worker in this phase — that's a
follow-up. The new worker is exercised by spawnllm's own tests (and Maestro
later).

## Dev workflow

Local `replace github.com/PivotLLM/spawnllm => ../spawnllm` during development;
public repo + `v0.1.0` tag at the end; drop the replace and verify a clean
`GOPROXY=direct` fetch builds + tests green (the deploy path).

---

## Checklist

### Slice 0 — module skeleton
- [ ] Create `/home/eric/source/spawnllm`; `go mod init github.com/PivotLLM/spawnllm` (`go 1.23.0`).
- [ ] Add `require github.com/PivotLLM/toolspec v0.1.0`.
- [ ] LICENSE (MIT, Tenebris), README, `doc.go`.
- [ ] `git init`; first commit.

### Slice 1 — move the clean provider code
- [ ] Move `protocoltypes` DTOs → spawnllm.
- [ ] Move `LLMProvider`/`CLIProvider`/`StatefulProvider`/`ThinkingCapable` interfaces.
- [ ] Move API clients (`http_provider` + 6 subpackages) and CLI clients (+ `cli_env`/`cli_options`/`codex_cli_credentials`).
- [ ] Move verified-clean support files; confirm each via `go list -deps` shows no ClawEh import.
- [ ] spawnllm builds + `go vet` clean.

### Slice 2 — the worker API
- [ ] `ProviderSpec` + `New(...Option)` (functional options, returns error, CLI-xor-API validation).
- [ ] `Run(ctx, messages)`: CLI = invoke+capture; API = lean tool loop over `[]toolspec.Tool`.
- [ ] `WithHTTPClient` so callers share one transport (hygiene; no per-worker transport churn).
- [ ] spawnllm unit tests: CLI run, API loop with a fake provider + a fake `toolspec.Tool` (tool-call → execute → final), max-iterations, error path, options validation.

### Slice 3 — rewire ClawEh
- [ ] go.mod: require spawnllm + local `replace`.
- [ ] `pkg/providers` → alias shim re-exporting moved types/clients (call sites untouched).
- [ ] Refactor `factory_provider`/`legacy_provider`/`dispatch` to build `ProviderSpec`/clients from config.
- [ ] `go build ./...`; `make test` green.
- [ ] **Guard test** (ClawEh side or spawnllm side): assert no `spawnllm/**` file imports `PivotLLM/ClawEh` (e.g. `go list -deps` check in a test or CI script).
- [ ] Confirm `agent_spawn` PrimaryOnly recursion guard still holds (existing tests pass + a focused assertion).

### Slice 4 — tests (both packages)
- [ ] spawnllm: table tests per provider kind constructed via `ProviderSpec`.
- [ ] ClawEh: factory/dispatch tests updated for the `ProviderSpec` mapping (on + off paths).
- [ ] `tests/test_mcpserver.sh` / `test.sh`: confirm provider-backed tool probes still pass; add/adjust as needed.
- [ ] Grep for stale references to moved symbols in code, tests, scripts, docs.

### Slice 5 — docs
- [ ] spawnllm README: ProviderSpec + worker usage, the toolspec injection contract, "no ClawEh imports" invariant.
- [ ] ClawEh `README.md`: note the spawnllm dependency + the dispatch architecture (policy in ClawEh, dispatch in spawnllm).
- [ ] ClawEh `docs/`: update tool-providers / subagents docs where the dispatch path is described.
- [ ] `CLAUDE.md` Key Architecture Notes: add the module boundary + cycle invariant.

### Slice 6 — publish & verify
- [ ] Push `PivotLLM/spawnllm` (public), tag `v0.1.0` (use the `securityguy@users.noreply.github.com` author email).
- [ ] ClawEh: drop `replace`, require `v0.1.0`, `GOPROXY=direct go mod tidy`, `go build ./...`, `make test` green (deploy path).
- [ ] Frontend unaffected (Go-only), but run `pnpm run build:backend` once to be safe.
- [ ] PR to `main`; remove this plan doc (or keep until merged).

## Risks & mitigations
- **Hidden config/auth coupling in a "support" file** → check `go list -deps` per file before moving; leave coupled files in ClawEh.
- **Import cycle creeps back** → the guard test on `spawnllm/**` imports.
- **Behavior drift from the move** → aliases keep call sites identical; `make test` between slices; deploy-path fetch verification at the end.
- **Scope creep into runAgentLoop** → explicitly out of scope this phase.
