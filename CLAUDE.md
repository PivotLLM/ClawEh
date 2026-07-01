# ClawEh — Project Instructions for Claude Code

## Project Status
**Unreleased** — no backwards compatibility required. Remove deprecated code rather than retaining it.

**Current working state (2026-06-30):** branch `feature/gateway-compat` contains all of
`origin/main` (setup wizard etc.) plus the device-gateway work (OpenClaw Gateway protocol for
the Rabbit R1 and the "Claw to Talk" Android voice app — see the Device Gateway section).
`main` is an ancestor, so the branch is fast-forward-mergeable; open a PR / merge when ready.
The device gateway is working with both the R1 and the Android app on the local test instance.

## What This Is
ClawEh is an independent Go project forked from sipeed/picoclaw on 2026-03-20.
- Module: `github.com/PivotLLM/ClawEh`
- Binary: `claw` (main.go at repo root) — the gateway, the WebUI HTTP layer, the session
  API, and the embedded frontend all share one process and one HTTP mux on
  `cfg.Gateway.Port` (default `18790`). There is no longer a separate
  `claw-launcher` / `claw-web` binary.
- Data dir constant: `global.DefaultDataDir` = `.claw` (pkg/global/defaults.go)
- Env override constant: `global.EnvVarHome` = `CLAW_HOME`
- Version/name constants: pkg/global/version.go

This is **not** a picoclaw fork for upstream PR purposes — it is an independent project.
Upstream picoclaw docs are archived in `historical/`.

## Build & Install
```
make test        # runs tests
```
To build and deploy: run `update-claw.sh` (on PATH). It builds the binary, stops claw, installs, and restarts. Do not run build/install commands directly.

## Key Architecture Notes
- **Shared modules**: the tool contract lives in `github.com/PivotLLM/toolspec`; the LLM-dispatch core (provider clients + the tool loop) lives in `github.com/PivotLLM/spawnllm`. `pkg/global` and `pkg/providers` are thin alias shims re-exporting them under the historical names, so call sites are unchanged. **Invariant: spawnllm imports only toolspec + stdlib (+ provider SDKs) — never ClawEh.** Tools (incl. the spawn tool) are *injected* as `toolspec.ToolDefinition`s, so the runtime re-entry (spawnllm runs a tool → `agent_spawn` → spawnllm) is not an import cycle; `agent_spawn` being `PrimaryOnly` prevents recursion. Guard: `pkg/providers/cycle_guard_test.go`. Policy (model selection, fallback, cooldown, config, results handling) stays in ClawEh. spawnllm logs route into ClawEh's logger via `installSpawnllmLogging` (`spawnllm/logger.SetBackend`).
- **Providers**: claude-cli, codex-cli, gemini-cli use subprocess execution. Timeout via `request_timeout` per-model config → `WithTimeout` constructors in factory. The client implementations live in spawnllm; ClawEh's `factory_provider.go`/`dispatch.go`/`fallback.go`/`cooldown.go` map config → providers and own the policy.
- **Cron**: mtime-based reload from disk; only saves when jobs are due. Prevents CLI/service race.
- **Error classifier**: uses `errors.Is(err, context.DeadlineExceeded)` to trigger fallback chain.
- **Multiple Telegram bots**: each `telegram_bots[].id` → channel `telegram-<id>`.
- **Agents**: named agents with separate workspaces; bindings route channels to agents.
- **Systemd**: `claw install` generates the unit and bakes the installer's live `PATH` into `Environment=PATH=` (target bin dir + current `PATH` + standard system dirs) — systemd does not expand `$HOME`/`~`/`%h` in `Environment=`, so paths must be absolute, which capturing the live PATH handles. The extra home-dir entries (node/pnpm/nvm, CLI-agent bins) are **not required to run ClawEh** — they are only needed to support **CLI-based providers** (claude-cli, codex-cli, gemini-cli) and tools that shell out (e.g. MCP via `npx`, skills); a core gateway using HTTP providers needs none of them. Re-run `claw install` if your node/nvm path changes. Set `CLAW_HOME` only for a non-default data dir (defaults to `~/.claw`); the app writes its own log to `$CLAW_HOME/logs/claw.log` — no `StandardOutput`/`StandardError` redirection needed.

## Device Gateway (external devices: Rabbit R1, voice apps)
Speaks the **OpenClaw Gateway WebSocket protocol** so hardware/voice clients pair and chat.
Code: `pkg/channels/device/` (protocol in `server.go`, listener/bus bridge in `gateway.go`,
read surface in `agentquery.go`); agent-loop wiring in `internal/gateway/device_query.go`.
**Full protocol + findings: `docs/device-gateway-protocol.md`.** Own listener on
`channels.device` (default port `18791`), separate from the WebUI/admin port.

Status: **working** with the Rabbit R1 (`mode=node`) and the "Claw to Talk" Android app
(`com.alvin.clawtotalk`, `mode=cli`/operator) on a local test instance. When testing against a
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
- **Auth:** a long 32-byte token (in the QR, for the R1) OR a typeable 5-word BIP39
  `word_token` passphrase (for apps), both constant-time; plus per-device Ed25519 pairing
  approval (cryptographic — locks to that install). Removing a paired device revokes its tokens.
- **Agent selection / session isolation:** the client encodes the selected agent as the session
  key's 2nd segment (`agent:<id>:<peer>:<profile>`). Operator keys are honored verbatim
  (per-profile isolation + `chat.history` reads the same key); node clients are isolated
  per-device (`agent:<defaultId>:device:<deviceId>`). Mechanism: `metadata["session_key"]` +
  `metadata["preresolved_agent_id"]`. `agents.list` falls back to the id as the display name
  (clients hide name-less agents).
- No permessage-deflate (disabled end-to-end); the OpenClaw `agent` event schema is
  `{runId, seq, stream, ts, data}` with no top-level `status` (clients default it to "unknown").

## Testing — always keep tests in sync (do not skip this)
- A change is not done until its tests are updated AND passing. Run `make test` after every change.
- **Add tests for new behavior.** New config flags, gating, and branches need a test for both the on and off paths — not just a tweak that makes existing tests compile.
- **Keep test fixtures in sync with renames/refactors.** When tool names, config keys, or APIs change, grep the whole repo (including `*_test.go`, `test.sh`, `tests/`) and update every reference. A rename that compiles can still break integration tests.
- **MCP integration tests are part of the suite.** `test.sh` runs `tests/test_mcpserver.sh` via the external `probe` binary against an ephemeral gateway. Every provider tool must be exposed in the test config and probed: success for hermetic tools, graceful-error probes for network/hardware/LLM tools (web, skill, agent_spawn, hw). Add a probe case when you add a tool.
- After implementing, do a final grep for the old name/symbol to confirm nothing stale remains in code, tests, scripts, or docs.

## Workflow Rules
- Never commit or push without explicit user instruction.
- Never push directly to main — use feature branches + PRs.
- Always compile after edits before declaring done: `go build ./...` for Go changes, and `cd web/frontend && pnpm run build:backend` for frontend/TypeScript changes. The frontend bundle lands in `web/backend/dist`, which is embedded by `web/backend/embed.go` into the merged claw binary.
- When investigating a problem, report findings and wait for approval before implementing.
- Keep responses short and direct — no preamble or summaries.
- Use Alice and Bob as example agent names in all docs/examples (never other names without asking the user first)
