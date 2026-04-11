# ClawEh — Project Instructions for Claude Code

## Project Status
**Unreleased** — no backwards compatibility required. Remove deprecated code rather than retaining it. Use caution to ensure that claw-launcher remains aligned with changes in claw

## What This Is
ClawEh is an independent Go project forked from sipeed/picoclaw on 2026-03-20.
- Module: `github.com/PivotLLM/ClawEh`
- Binaries: `claw` (cmd/claw) and `claw-launcher` (web/backend)
- Data dir constant: `global.DefaultDataDir` = `.claw` (pkg/global/defaults.go)
- Env override constant: `global.EnvVarHome` = `CLAW_HOME`
- Version/name constants: pkg/global/version.go

This is **not** a picoclaw fork for upstream PR purposes — it is an independent project.
Upstream picoclaw docs are archived in `historical/`.

## Build & Install
```
make test        # runs tests
```
To build and deploy: run `update-claw.sh` (on PATH). It builds both binaries, stops claw, installs, and restarts. Do not run build/install commands directly.

## Key Architecture Notes
- **Providers**: claude-cli, codex-cli, gemini-cli use subprocess execution. Timeout via `request_timeout` per-model config → `WithTimeout` constructors in factory.
- **Cron**: mtime-based reload from disk; only saves when jobs are due. Prevents CLI/service race.
- **Error classifier**: uses `errors.Is(err, context.DeadlineExceeded)` to trigger fallback chain.
- **Multiple Telegram bots**: each `telegram_bots[].id` → channel `telegram-<id>`.
- **Agents**: named agents with separate workspaces; bindings route channels to agents.
- **Systemd**: service needs `Environment=PATH=/home/eric/.local/bin:/usr/local/bin:/usr/bin:/bin`. Set `CLAW_HOME` only if using a non-default data directory (defaults to `~/.claw`). The app writes its own log to `$CLAW_HOME/logs/claw.log` — no `StandardOutput`/`StandardError` redirection needed.

## Workflow Rules
- Never commit or push without explicit user instruction.
- Never push directly to main — use feature branches + PRs.
- Always compile after edits before declaring done: `go build ./...` for Go changes, and `cd web/frontend && pnpm run build:backend` for frontend/TypeScript changes. Both must pass clean.
- When investigating a problem, report findings and wait for approval before implementing.
- Keep responses short and direct — no preamble or summaries.
- Use Alice and Bob as example agent names in all docs/examples (never other names without asking the user first)
