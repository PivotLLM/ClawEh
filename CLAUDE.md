# ClawEh — Project Instructions for Claude Code

## Project Status
**Unreleased** — no backwards compatibility required. Remove deprecated code rather than retaining it.

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
- **Providers**: claude-cli, codex-cli, gemini-cli use subprocess execution. Timeout via `request_timeout` per-model config → `WithTimeout` constructors in factory.
- **Cron**: mtime-based reload from disk; only saves when jobs are due. Prevents CLI/service race.
- **Error classifier**: uses `errors.Is(err, context.DeadlineExceeded)` to trigger fallback chain.
- **Multiple Telegram bots**: each `telegram_bots[].id` → channel `telegram-<id>`.
- **Agents**: named agents with separate workspaces; bindings route channels to agents.
- **Systemd**: service needs `Environment=PATH=/home/eric/.local/bin:/usr/local/bin:/usr/bin:/bin`. Set `CLAW_HOME` only if using a non-default data directory (defaults to `~/.claw`). The app writes its own log to `$CLAW_HOME/logs/claw.log` — no `StandardOutput`/`StandardError` redirection needed.

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
