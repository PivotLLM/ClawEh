# ClawEh — Project Instructions for Claude Code

## Project Status
**Released.** Config schemas, tool names, API shapes, and the device-gateway
protocol are now things other people depend on.

This reverses the previous policy, under which deprecated code was removed on
sight. Do **not** write compatibility shims, migration paths, or aliases on your
own initiative, and do **not** remove or rename something released on the
assumption that breaking it is fine. Both are now decisions for the user:

- Before adding backwards-compatible handling (accepting an old config key,
  keeping a renamed field working, tolerating an old protocol version), **ask
  first.** Compatibility code is load-bearing once written and rarely removed.
- Before a breaking change (renaming a config key or tool, changing a JSON
  shape, dropping a flag or endpoint), **ask first**, and say what would break.

Version lives in `app/app.go`; bumping it is a normal change, tagging is not
(see Workflow Rules).

## Changelog — update it as part of the change, not afterwards

`CHANGELOG.md` follows Keep a Changelog. A change that alters what an operator
or integrator sees is **not complete until its entry is written**, in the same
commit as the code. Treat it exactly like the tests: not a separate chore.

**Write an entry when the change touches** a config key (added, renamed,
removed, or its default), a tool name or its arguments, an HTTP/MCP/gateway
request or response shape, a CLI command or flag, a default that alters
behaviour on upgrade, a security-relevant behaviour, or a user-visible message.

**Do not write an entry for** refactors, moved packages, added or changed tests,
internal comments, or anything else invisible from outside the repository. A
changelog padded with churn stops being read, which costs more than a missing
line.

Rules:

- Add to the section for the version **currently in development** — the topmost
  `## [x.y.z]`, which is the version in `app/app.go`. There is no `[Unreleased]`
  section; do not create one. Entries accumulate under that version heading
  until the user cuts the release.
- Use the Keep a Changelog headings, in this order, omitting empty ones:
  `Security`, `Added`, `Changed`, `Deprecated`, `Removed`, `Fixed`.
- Prefix anything that breaks an existing install with **BREAKING**, and give the
  migration in the entry: the config to set, the command to run, the value that
  restores the old behaviour. A reader hitting it after an upgrade must be able
  to act without reading the diff.
- Write for someone who has not seen the code. Name the config key or tool, say
  what changed, and say why it matters — not the function you edited.
- Releases are cut by the user on macOS (the macOS binaries are signed there),
  not on this machine and not by you. Do not create the git tag (see Workflow
  Rules).
- **After a release is cut, the version must be bumped before the next entry is
  written** — otherwise new work accumulates under a heading that has already
  shipped. When you are asked to record a change and the topmost version has
  already been released, say so and remind the user to bump `version` in
  `app/app.go`; the new section, and the link refs at the bottom of the file,
  follow that bump.

## What This Is
ClawEh is an independent Go project forked from sipeed/picoclaw on 2026-03-20.
- Module: `github.com/PivotLLM/ClawEh`
- Binary: `claw` (main.go at repo root) — the gateway, the WebUI HTTP layer, the session
  API, and the embedded frontend all share one process and one HTTP mux on
  `cfg.Gateway.Port` (default `18790`). There is no longer a separate
  `claw-launcher` / `claw-web` binary.
- Data dir constant: `global.DefaultDataDir` = `.claw` (global/defaults.go)
- Env override constant: `global.EnvVarHome` = `CLAW_HOME`
- Version/name/tagline/copyright: `app/app.go` (all unexported — read them through
  `app.Version()` / `app.SemVer()` / `app.Name()` / `app.TagLine()` / `app.Copyright()`).

This is **not** a picoclaw fork for upstream PR purposes — it is an independent project.
Upstream picoclaw docs are not carried in this repo.

## Build & Install
```
make test        # runs tests
```
To build and deploy **production**: run `update-claw.sh` (on PATH). It builds the binary, stops the service, installs, and restarts. Do not run build/install commands directly for prod.

Systemd units: `claw-ai.service` is **production** — never build to, install to, or restart it directly; production deploys go through `update-claw.sh` only. `claw-dev.service` is the local **dev** instance for iterating in a developer account; build the binary and restart `claw-dev.service` for local testing. Never touch production or `update-claw.sh` when testing.

## Key Architecture Notes
- **Shared modules**: the tool contract lives in `github.com/PivotLLM/toolspec`; the LLM-dispatch core (provider clients + the tool loop) lives in `github.com/PivotLLM/spawnllm`. `global` and `providers` are thin alias shims re-exporting them under the historical names, so call sites are unchanged. **Invariant: spawnllm imports only toolspec + stdlib (+ provider SDKs) — never ClawEh.** Tools (incl. the spawn tool) are *injected* as `toolspec.ToolDefinition`s, so the runtime re-entry (spawnllm runs a tool → `agent_spawn` → spawnllm) is not an import cycle; runaway recursion is bounded by `agents.defaults.max_subagent_depth` (default 3, shared by `agent_spawn` and Maestro dispatch — see `tools/agents/depth.go`), which replaced the old blanket `PrimaryOnly` restriction. Guard: `providers/cycle_guard_test.go`. Policy (model selection, fallback, cooldown, config, results handling) stays in ClawEh. spawnllm logs route into ClawEh's logger via `installSpawnllmLogging` (`spawnllm/logger.SetBackend`).
- **Providers**: claude-cli, codex-cli, gemini-cli use subprocess execution. Timeout via `request_timeout` per-model config → `WithTimeout` constructors in factory. The client implementations live in spawnllm; ClawEh's `factory_provider.go`/`dispatch.go`/`fallback.go`/`cooldown.go` map config → providers and own the policy.
- **Cron**: mtime-based reload from disk; only saves when jobs are due. Prevents CLI/service race.
- **Error classifier**: uses `errors.Is(err, context.DeadlineExceeded)` to trigger fallback chain.
- **Multiple Telegram bots**: each `telegram_bots[].id` → channel `telegram-<id>`.
- **Agents**: named agents with separate workspaces; bindings route channels to agents.
- **Systemd**: `claw install` generates the unit and bakes the installer's live `PATH` into `Environment=PATH=` (target bin dir + current `PATH` + standard system dirs) — systemd does not expand `$HOME`/`~`/`%h` in `Environment=`, so paths must be absolute, which capturing the live PATH handles. The extra home-dir entries (node/pnpm/nvm, CLI-agent bins) are **not required to run ClawEh** — they are only needed to support **CLI-based providers** (claude-cli, codex-cli, gemini-cli) and tools that shell out (e.g. MCP via `npx`, skills); a core gateway using HTTP providers needs none of them. Re-run `claw install` if your node/nvm path changes. Set `CLAW_HOME` only for a non-default data dir (defaults to `~/.claw`); the app writes its own log to `$CLAW_HOME/logs/claw.log` — no `StandardOutput`/`StandardError` redirection needed.

## Device Gateway (external devices: Rabbit R1, voice apps)
Speaks the **OpenClaw Gateway WebSocket protocol** so hardware/voice clients pair and chat.
Code: `channels/device/` (protocol in `server.go`, listener/bus bridge in `gateway.go`,
read surface in `agentquery.go`); agent-loop wiring in `internal/gateway/device_query.go`.
**Full protocol + findings: `docs/device-gateway-protocol.md`.** Own listener on
`channels.device` (default port `18791`), separate from the WebUI/admin port.

Status: **working** with the Rabbit R1 (through the Rabbit agent; the gateway sees a
`mode=node` client) and the "Claw to Talk" Android app (`com.alvin.clawtotalk`,
`mode=cli`/operator) on a local test instance. When testing against a
non-prod instance, build and restart that instance's service directly; never touch the
production install or `update-claw.sh`.

Hard-won learnings (don't relearn these):
- **A turn = immediate ack + async events.** `chat.send` returns `{runId, status:"started"}`
  **immediately** (runId = the client's `idempotencyKey`); the reply is delivered later as
  events. The ack must NOT carry the result or block on the run, or strict clients time out.
- **Emit BOTH event families.** Operator clients (the Android app) consume only **`agent`**
  events — they accumulate `data.text` from `stream:"assistant"` and complete the turn on
  `stream:"lifecycle"` `data.phase:"end"`, ignoring `chat` entirely (found by decompiling the
  Hermes bundle). The **R1 (node) uses both**: `chat`/`final` for the on-screen transcript and
  the `agent` `assistant` text for its **speech** pipeline (`lifecycle/end` completes it).
- **Order: `agent` stream BEFORE `chat` final.** `emitChatReply` emits `agent:assistant` →
  `agent:lifecycle/end` → `chat:final`. If `chat`/`final` goes first, the R1 marks the turn
  complete and paints the transcript **without speaking** — reply shows but is silent. Sending
  the `agent` stream first (real gateway's stream-then-finalize order) makes it speak + display.
- **Partial streaming (opt-in per channel).** Streaming-capable channels (`StreamCapable`, the
  device gateway) get partial text as the model generates: coalesced `chat:delta` + `agent:assistant`
  deltas, each with an **incrementing per-run payload `seq`** (reused seq → clients drop it as a
  duplicate; that's why the R1 once spoke only the first chunk). Streamed `agent:assistant`
  `data.text` carries the **increment**, not the running total. `emitChatReply` skips the full-text
  `agent:assistant` event for a streamed run (deltas already sent it) — avoids double-speak.
  `streamToolNarration` (`stream_coalescer.go`, default on) is the off switch. spawnllm HTTP
  providers stream via SSE; CLI providers return the whole reply (no deltas).
- **Auth:** a long 32-byte token (in the QR, for the R1) OR a typeable 5-word BIP39
  `word_token` passphrase (for apps), both constant-time; plus per-device Ed25519 pairing
  approval (cryptographic — locks to that install). Removing a paired device revokes its tokens.
- **Agent selection / session scope:** the client encodes the selected agent as the session
  key's 2nd segment (`agent:<id>:<peer>:<profile>`); node clients send the `main` sentinel and
  use their per-device assignment (else the default agent). Which *conversation* the turn joins
  is decided by `session.session_scope`, not by the transport: under **`unified` (default) a
  device joins the selected agent's main session** (`agent:<id>:main`) — one agent, one history,
  one memory across the R1, the app, Slack, Telegram, and MCP service tokens. Isolating modes
  keep the old behavior (operator keys verbatim; node clients per-device). `chat.history`
  resolves through the same rule as `chat.send`. Single source of truth:
  `routing.ResolveDeviceSessionKey` / `routing.ResolveServiceSessionKey`; mechanism:
  `metadata["session_key"]` + `metadata["preresolved_agent_id"]`. `agents.list` falls back to
  the id as the display name (clients hide name-less agents).
  **Isolation is a property of the agent** — a separate agent (optionally `cogmem: false`), not
  a separate channel.
- **`/agent` command (node clients):** node clients (R1) switch assistants by typing `/agent`
  (list), `/agent <name-or-id>` (switch), or `/agent default` (reset). `handleChatSend`
  intercepts it and persists to `paired_devices.agent_id` via `SetDeviceAgent` — the same field
  `sessionScopeKey` reads, so it survives restarts. Reply goes through the normal event path.
- No permessage-deflate (disabled end-to-end); the OpenClaw `agent` event schema is
  `{runId, seq, stream, ts, data}` with no top-level `status` (clients default it to "unknown").

## Testing — always keep tests in sync (do not skip this)
- A change is not done until its tests are updated AND passing. Run `make test` after every change.
- **Add tests for new behavior.** New config flags, gating, and branches need a test for both the on and off paths — not just a tweak that makes existing tests compile.
- **Keep test fixtures in sync with renames/refactors.** When tool names, config keys, or APIs change, grep the whole repo (including `*_test.go`, `test.sh`, `tests/`) and update every reference. A rename that compiles can still break integration tests.
- **MCP integration tests are part of the suite.** `test.sh` runs `tests/test_mcpserver.sh` via the external `probe` binary against an ephemeral gateway. Every provider tool must be exposed in the test config and probed: success for hermetic tools, graceful-error probes for network/LLM tools (web, skill, agent_spawn). Add a probe case when you add a tool.
- After implementing, do a final grep for the old name/symbol to confirm nothing stale remains in code, tests, scripts, or docs.

### WebUI regression suite — run it for any significant frontend change

`make test` and `test.sh` do NOT exercise the running WebUI. A browser suite
does, and it must be run for any significant change to `web/frontend`, to the
`/api/*` handlers behind it, or to anything that alters gateway startup,
readiness or config reload:

```
make build && cp build/claw ~/bin/claw && sudo systemctl restart claw-dev
until curl -sf http://127.0.0.1:8077/ready >/dev/null; do sleep 1; done
node tests/frontend-e2e.mjs
```

- **The plan is `docs/webui-test-plan.md`** — 64 numbered steps, each with a
  process and an expected result, followable by hand. `tests/frontend-e2e.mjs`
  executes it and prints the same step IDs. Keep the two in step: a step added
  to one belongs in the other.
- **Dev only.** Groups F and G write configuration (they create an agent and
  edit a field, and revert both). The runner refuses port 18790 unless
  `--allow-prod` is given. Never point it at production.
- **Wait for `/ready`, not `/health`.** `/health` answers as soon as the port
  is open; `/ready` waits for the channels. Starting early makes steps fail for
  no reason.
- **When a step fails, decide first whether the PLAN or the PRODUCT is wrong.**
  Both happen. It has caught real defects (`/ready` stuck at 503 forever, an
  unlabelled delete button) and has itself been wrong (sidebar entries are
  disclosure controls, not links; the agent rail labels by `name || id`).
  Fixing a correct test to match broken behaviour is the one outcome to avoid.
- **If you are unsure whether a change is significant enough to warrant a run,
  ask the user.** It takes a couple of minutes; a silent WebUI regression does
  not announce itself.
- Ordinary Go-only changes do not need it. `make check` also runs the frontend
  typecheck, oxlint and vitest, none of which load a page.

## Workflow Rules
- Never commit or push without explicit user instruction.
- Never push directly to main — use feature branches + PRs.
- **Never create, move, or delete git tags.** Tags are cut by the user's build
  process when binaries are uploaded — they are release markers, not commit
  markers. Bumping the `version` constant in `app/app.go` is a normal code
  change and is fine when asked; tagging that version is not. The build does not
  derive a version from `git describe` — `app/app.go` holds the identity (name, tagline,
  copyright, version) and is the single source of truth, with the Makefile
  stamping only build metadata (commit, timestamp, toolchain).
  Everything there is unexported: read it through `app.Version()` (display:
  `0.4.69+58d98993` — "+" is SemVer build metadata, ignored when comparing),
  `app.SemVer()` (protocol handshakes: `0.4.69`),
  `app.Name()`, `app.TagLine()`, `app.Copyright()`. Build tooling greps the
  `version = "..."` line. See `~/.claude/standards/go-standards.md`
  § Versioning and Copyright.
- Always compile after edits before declaring done: `go build ./...` for Go changes, and `cd web/frontend && pnpm run build:backend` for frontend/TypeScript changes. The frontend bundle lands in `web/backend/dist`, which is embedded by `web/backend/embed.go` into the merged claw binary.
- When investigating a problem, report findings and wait for approval before implementing.
- Keep responses short and direct — no preamble or summaries.
- Use Alice and Bob as example agent names in all docs/examples (never other names without asking the user first)
