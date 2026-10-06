# ClawEh — Project Instructions for Claude Code

## Keep this file current — that is your job

This file is read at the start of every session and trusted. When a change you
make alters anything it states — a make target or what it runs, a command
sequence, a port, a path, a count (such as the number of browser checks), a
package or its role, a workflow rule — update the relevant passage in the
**same change**, the way tests and the changelog are updated. A stale
instruction here costs more than a missing one, because the next session
follows it. When you notice a passage that no longer matches the code, fix it
or say so; do not work around it silently.

## Project Status
**Released.** Config schemas, tool names, API shapes, and the device listener
protocol are now things other people depend on.

This reverses the previous policy, under which deprecated code was removed on
sight. Do **not** write compatibility shims, migration paths, or aliases on your
own initiative, and do **not** remove or rename something released on the
assumption that breaking it is fine. Both are now decisions for the maintainer:

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
removed, or its default), a tool name or its arguments, an HTTP/MCP/device-protocol
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
  until the release is cut.
- Use the Keep a Changelog headings, in this order, omitting empty ones:
  `Security`, `Added`, `Changed`, `Deprecated`, `Removed`, `Fixed`.
- Prefix anything that breaks an existing install with **BREAKING**, and give the
  migration in the entry: the config to set, the command to run, the value that
  restores the old behaviour. A reader hitting it after an upgrade must be able
  to act without reading the diff.
- Write for someone who has not seen the code. Name the config key or tool, say
  what changed, and say why it matters — not the function you edited.
- Releases are cut by the release maintainer on the signing host (the macOS
  binaries are signed there), never from a development session. Do not create
  the git tag (see Workflow Rules).
- **After a release is cut, the version must be bumped before the next entry is
  written** — otherwise new work accumulates under a heading that has already
  shipped. When you are asked to record a change and the topmost version has
  already been released, say so and ask for `version` in `app/app.go` to be
  bumped; the new section, and the link refs at the bottom of the file, follow
  that bump.

## What This Is
ClawEh is an independent Go project forked from sipeed/picoclaw on 2026-03-20.
- Module: `github.com/PivotLLM/ClawEh`
- Binary: `claw` (main.go at repo root) — the agent runtime, the WebUI HTTP layer, the session
  API, and the embedded frontend all share one process and one HTTP mux. The mux is
  served on **two listeners** (`internal/gateway/httphost.go`): plain HTTP on
  `gateway.host:gateway.port` (default loopback, `127.0.0.1` and `[::1]` on `18790`; a
  network host is allowed but unencrypted and marked by the report) and HTTPS on
  `gateway.tls_port` (default `18443`) placed by `gateway.tls.mode` — `all` (default,
  every interface), `localhost` or `off` — with a self-signed or operator-supplied
  certificate managed by `internal/tlscert` (`claw tls` inspects it; the WebUI uses
  `GET /api/tls`, `POST /api/tls/validate`, `POST /api/tls/regenerate`; see
  `docs/tls.md`). Where each binds is defined once in `config/gateway_listeners.go`
  (`HTTPBindHosts`, `HTTPSBindHosts`, the URL helpers) and shared by startup,
  `claw status`, the API and the report. There is no longer a separate
  `claw-launcher` / `claw-web` binary.
- **Operator login:** every `/api/*` request and `/webui/ws` require the admin
  account created by `claw install` before it starts the service, or by
  `claw admin` (`internal/admincmd`; credentials, argon2id, the file format and
  the shared prompt/`CLAW_ADMIN_USER`+`CLAW_ADMIN_PASSWORD` logic,
  `admin.EnsureAccount`, in `internal/admin`; sessions and the middleware in
  `web/backend/middleware/auth*.go`). Loopback is not exempt. `<CLAW_HOME>/credentials.json`
  is the only account; there is no default and no WebUI path to create one. See
  `docs/webui-auth.md`. The handler chain on the listener is IP allowlist → Host
  check (421) → cross-origin protection → security headers → login → body limit → mux.
- **One configuration:** `config.Store` (`config/store.go`) is the in-memory config the
  WebUI API reads (`h.currentConfig()`) and writes (`h.updateConfig(fn)`: lock → clone →
  mutate → resolve secret refs → validate listeners → atomic save → swap); ClawEh
  derives its pruned running copy from it at boot and on every reload (`store.Reload()`).
  Handlers must not call `config.LoadConfig` themselves. Secret fields may hold
  `env:NAME` / `file:/path` references (`config/secrets.go`), resolved at load and written
  back on save; unknown keys are warned at load (`config/unknown_keys.go`).
- **Startup guards:** `internal/perms.Enforce` runs before the config loads — `CLAW_HOME`
  and every folder under it become 0700, secrets/DBs 0600, and a group/other-readable `config.json` aborts startup
  with the `chmod 600` to run. Everything ClawEh creates under `CLAW_HOME` is 0700/0600.
- **Audit log:** `internal/audit` (`<CLAW_HOME>/internal/audit.db`) records tool calls, config
  writes and logins; every turn carries a `turn_id` on its log lines. See `docs/audit.md`.
- **Fail fast:** if the HTTP listener, the MCP host server or the agent loop dies after
  start, `internal/gateway/fatal.go` alerts, shuts down cleanly and exits 3 so systemd
  restarts the process.
- **Backup:** `internal/backup` writes one `claw-backup-<ts>.tar.gz` nightly (every SQLite
  DB via `VACUUM INTO`, `internal/`, credentials, TLS); `claw backup` / `claw restore`. See
  `docs/backup.md`.
- Data dir constant: `global.DefaultDataDir` = `.claw` (global/defaults.go)
- Env override constant: `global.EnvVarHome` = `CLAW_HOME`
- Data dir layout (README "File layout"): `internal/` holds claw's own state
  (token stores, `gateway.db`, fusion tokens, the ACP identity, `audit.db`,
  `claw.pid`, `claw.lock`, and the temporary agents: `internal/temp/<uuid>/` and
  their list `internal/temp_agents.json`, both left out of backups;
  `config.InternalPath()`,
  `global.InternalDir`), `cli/` is the CLI providers' working dir when a model sets no
  workspace (`config.CLIPath()`), `skills/` is the only shared skills root, `common/` the
  default common dir (`config.ResolveCommonDir()`); relative MCP `env_file` paths resolve
  against `CLAW_HOME`. There is no `agents/default` pseudo-agent. `internal/layout.Prepare`
  creates these directories at every start; it does not migrate an older layout.
- Version/name/tagline/copyright: `app/app.go` (all unexported — read them through
  `app.Version()` / `app.SemVer()` / `app.Name()` / `app.TagLine()` / `app.Copyright()`).
  The two release-signing public keys are `app/keys.go` (`app.ReleasePublicKeys()`).

This is **not** a picoclaw fork for upstream PR purposes — it is an independent project.
Upstream picoclaw docs are not carried in this repo.

## Design principles for the WebUI

Help the user avoid mistakes. The reviews that produced these rules each
started with an operator surprised by their own configuration.

- **A setting that only works together with another is shown with that
  dependency at both places.** When a value is entered somewhere but ignored
  because of a setting elsewhere, the page where it was entered says so and
  links to the page that decides (the Agents page warns about a model whose
  bypass flag is dropped because the provider's setting is off).
- **Enabling a capability reads as allowing it.** A checkbox that turns a
  power on is labelled "Allow …" ("Allow CLI to bypass restrictions"), never
  as a neutral noun, so the operator sees that something is being granted.
- **Anything the user is told about names the thing.** A reply or alert about
  an agent names the agent; one about a model names the model. "The CLI
  declined" in a channel shared by several assistants tells the operator
  nothing; "Karen: the Claude CLI declined…" does.
- **An ignored value is visible where it lives, not only in a log.** Logs are
  for after the fact; the WebUI is where the mistake is being made.

## Build & Install
```
make build       # build the binary (embeds the frontend bundle)
make test        # the full gate: generate, format check, vet, golangci-lint,
                 # govulncheck (pinned, installed into bin/, needs network),
                 # then test.sh: the Go suite with -race and a coverage floor,
                 # the frontend typecheck/lint/unit tests and the MCP server
                 # integration checks. Rewrites nothing and exits non-zero on
                 # any failure, so a pipeline can gate on it. `make check` is
                 # an alias. Failures are re-printed at the end and saved to
                 # .test-failures.log (see docs/test.md).
go test ./...    # the fast Go-only loop while iterating
make test-maestro-host   # Maestro's MCP suite against a live ClawEh (own
                         # target: binds ports; needs probe, jq, zip)
make check-webui         # the browser suite against a running dev instance
```
Production and development instances are deployed differently; the service
names, ports and deploy procedure for a given machine belong in
`CLAUDE.local.md` (not committed). Never build to, install to, or restart a
production instance directly; test against a dev instance.

**Release artefacts (cut by the release maintainer with b9):** `b9 release <repo>` builds every target in `b9.yaml`, signs and notarizes the macOS binaries, packages the archives, writes `checksums.txt` and signs it with minisign (`signing.minisign` in `b9.yaml` names the public key; the secret key lives on the release host), verifies that signature against the same public key, then tags and uploads everything. The Makefile's `release-checksums` and `release-sign` targets are the manual fallback for a hand-built release. `claw upgrade` refuses a release without a valid `checksums.txt.minisig`, verified against the two minisign public keys in `app/keys.go` (`app.ReleasePublicKeys()`: a current and a next key, so a key can be rolled; the rotation steps are in that file). Both slots are filled.

## Key Architecture Notes
- **Shared modules**: the tool contract lives in `github.com/PivotLLM/toolspec`; the LLM-dispatch core (provider clients + the tool loop) lives in `github.com/PivotLLM/spawnllm`. `global` and `providers` are thin alias shims re-exporting them under the historical names, so call sites are unchanged. **Invariant: spawnllm imports only toolspec + stdlib (+ provider SDKs) — never ClawEh.** Tools (incl. the spawn tool) are *injected* as `toolspec.ToolDefinition`s, so the runtime re-entry (spawnllm runs a tool → `agent_spawn` → spawnllm) is not an import cycle; runaway recursion is bounded by `agents.defaults.max_subagent_depth` (default 3, shared by `agent_spawn` and Maestro dispatch — see `tools/agents/depth.go`), which replaced the old blanket `PrimaryOnly` restriction. Guard: `providers/cycle_guard_test.go`. Policy (model selection, fallback, cooldown, config, results handling) stays in ClawEh. spawnllm logs route into ClawEh's logger via `installSpawnllmLogging` (`spawnllm/logger.SetBackend`). Cognitive memory lives in `github.com/PivotLLM/cogmem` (store, composer, consolidation, portable export, toolspec tools, and the `Session` the loop drives with `Observe`/`Recall`). **Same invariant: cogmem imports only toolspec + stdlib (+ SQLite) — never ClawEh.** The host side is `cogmemhost/` (the attachment loader that enforces file-tool permissions, the logging bridge via `cogmem/logger.SetBackend`, and the `config.MemoryConfig` → `cogmem.Settings` mapping); `tools/cogmem` mounts the module's tools under the `cogmem` namespace. Cogmem keeps its own inbox of unconsolidated messages, so it never reads the session archive; the one-time inbox backfill from the archive on upgrade is host code in `agent/memory_wiring.go`. The context engine lives in `github.com/PivotLLM/ctxengine` (transcript, archive, assembly, eviction, compaction, and the `session_*` tools; ClawEh imports its `memory` and `session` packages for the store). **Same invariant: ctxengine imports only spawnllm + toolspec + stdlib (+ SQLite) — never ClawEh.** The host side is in `agent/`: `ContextBuilder.PromptLayers` builds the system prompt layers the engine assembles, `compressModelCaller` (`agent/context_manager.go`) is the one `ModelCaller` that walks the summarization chain for both the engine and cogmem, `agent/llmcontext_logging.go` bridges its logger, and `tools/session` mounts its tools. The engine and cogmem never see each other: the loop observes messages into cogmem after each engine `Add*` and hands cogmem's `Recall` blocks to `Assemble` as injections.
- **Providers**: claude-cli, codex-cli, antigravity-cli (binary `agy`; `gemini-cli` is an accepted alias since Google deprecated it), cursor-cli use subprocess execution. Timeout via `request_timeout` per-model config → `WithTimeout` constructors in factory. The client implementations live in spawnllm; ClawEh's `factory_provider.go`/`dispatch.go`/`fallback.go`/`cooldown.go` map config → providers and own the policy. The CLIs' permission-bypass flags (`--dangerously-skip-permissions`, `--yolo`, …) are `BypassArgs` in `config/clis.go` and are passed **only** when the provider's `bypass_restrictions` is true (the WebUI checkbox *Bypass CLI restrictions*, default off); a bypass flag in `extra_args` is stripped when it is off. CLI, `shell_exec` and stdio-MCP child processes start from the `internal/childenv` allowlist, never the full service environment (CLIs additionally get their vendor's `ANTHROPIC_*`/`OPENAI_*`/… variables).
- **Configuration report**: the `report` package builds a config-derived inventory of what the install can do (a security assessment table, listeners, providers and models, tokens as set/not set, channels, per-agent tools and folder access, external services, devices, data, schedules) and renders it as PDF; `GET /api/report/pdf` serves it. The WebUI Check Up page (`/report`, labelled **Check Up** in the sidebar) shows the product identification line and the security assessment table inline (`GET /api/report/assessment`, JSON from the same collector) with a **Full report** button for the PDF. Neither output ever emits a secret value (guarded by tests). See `docs/report.md`.
- **Operator alerts**: `github.com/tenebris-tech/alerter` (queued, de-duplicated alerts; always logged, delivered on channels configured only by `ALERTER_*` variables or `~/.alerter`). ClawEh builds one in `internal/gateway/alerts.go` (app name, short hostname, `<CLAW_HOME>/logs/alerts.log` unless `ALERTER_LOG` is set) and hands it to the agent loop, which passes it to the cooldown tracker and MCP manager; the channel manager and cron service get it too. Channels get it through `BaseChannel.SetAlerter`/`Alert` (injected by the manager). Code with no owner (package singletons, free functions, per-registry providers) raises through the process default in the `alerts` package (`alerts.Send`), set once by ClawEh; tests capture it with `internal/testalerts.Install`. Prefer explicit injection wherever the constructor site has the alerter in hand. Every alert (title, event id, source) is listed in `ALERTS.md`; **add a row there when you add an alert.** Every alert is `alerter.Normal` priority; `Urgent` and `Emergency` mean "reach a person now, at any hour" and are not used by ClawEh. Never raise an alert above Normal without asking, and if one is agreed mark it `*` (Urgent) or `**` (Emergency) in the Priority column. `GET /api/gateway/alerts` tails the file; the Logs page shows it via its source selector. See `docs/alerts.md`.
- **Cron**: mtime-based reload from disk; only saves when jobs are due. Prevents CLI/service race.
- **Error classifier**: uses `errors.Is(err, context.DeadlineExceeded)` to trigger fallback chain.
- **Multiple Telegram bots**: each `telegram_bots[].id` → channel `telegram-<id>`.
- **Bus turn contract** (`bus/types.go`, `agent/loop_inbound.go` `runTurn`): the final reply answering an inbound message carries `OutboundMessage.Outcome` (`ok`/`error`/`cancelled`/`empty`; interim messages and SendResponse/async replies leave it empty). Inbound metadata `reply_required`=`1` forces exactly one final reply (even after `msg_send`, empty, failed or cancelled; nothing on shutdown), is never merged with other queued messages, and survives restart recovery (a give-up is an `error` reply); `spawn_depth`=N raises the turn's sub-agent depth (never lowers it, clamped to `max_subagent_depth`) and async results re-enter at the spawning turn's depth. A scheduled (cron) job runs at depth 0. Each turn records its depth on the session token (`SessionTokenStore.SetDepth`) and MCP tool calls with that token run at it, so CLI-provider agents are bounded too; service tokens run at 0. Tools manage temporary agents through `ToolDeps.Agents` (`tools.AgentServices`, `agent/agent_services.go`).
- **Agent messages** (`agent/agent_message.go`, `docs/agent-messaging.md`): `AgentLoop`
  is the `tools.Messenger` (`Ask`, `Whisper`) behind the `agent_message` tool
  (`tools/agents/message.go`, gated by `subagents.allow_agents`), the `/ask` and `/whisper`
  commands (sender must reach the target by routing: `routing.RouteResolver.Reaches`) and
  the forum. An ask is an inbound message on the internal `constants.AgentMessageChannel`
  (preresolved agent, `reply_required`, `spawn_depth`+1, the waiting agents in
  `ask_chain`); `runTurn` hands its final reply to the waiting `Ask`,
  never to the bus. Concurrency (`agent/turn_slot.go`): every turn except an asked one
  holds a `max_concurrent_turns` slot as a `turnSlot` on its context, and a wait inside
  it (an `Ask`, a request to a person) lends the slot (`lend`/`reclaim`: first wait lends,
  last reclaims; parallel waits are counted); asked turns take no slot. Whispers are held
  in memory and prepended in `runAgentLoop` (in `runHumanTurn` for a human agent) to the
  agent's next message.
- **Turn scope** (`tools/origin.go`): the ask chain rides the context and the MCP
  session token (`SetTurnScope`) like the sub-agent depth.
- **Shell permission**: whether an agent may run `shell_exec` is one per-agent
  switch, off by default: `shell_exec` named in its own `tools` list (WebUI "Allow
  shell commands") and not in `deny_tools` (`AgentConfig.IsToolAllowed`,
  `config.ShellExecTool`). A `"*"` or prefix entry never includes it, and there is no
  install-wide switch: `ToolsConfig.ToolEnabled` always admits it, a
  `tool_overrides.shell_exec` is ignored with a load WARN and a Check Up row, and the
  Tools page neither lists nor toggles it. Clones inherit it. There is no channel rule: it applies on every channel and to
  everything acting as the agent (clones, asks, forum turns, MCP session and service
  tokens). Fresh temporary and human agents have no tools. A refused call gets
  `tools.ShellNotAllowedMessage` ("Alice is not allowed to run shell commands."),
  named from `ToolRegistry.Owner` (`agent/shell_permission_test.go`).
- **Human agents** (`config/human.go`, `providers/human.go`, `agent/human.go`; `docs/human-agents.md`): an agent whose model (matched by `model_name`) is on a provider with protocol `human` represents a person. It takes work only from asks: turns on `constants.AgentMessageChannel` with `reply_required`; the reply goes back to the asker through `runTurn`'s ask path; `runHumanTurn` drops anything else (a person writing to it is told "Bob only answers questions from agents."; device agent lists leave it out), and cron (`tools/schedule`) and `HandleExternalMessage` (`ErrHumanAgent`) refuse it at the source. An ask posts the latest user message to the agent's one default-binding chat (`CronTarget`) and returns the person's next text there (`askHuman`: one request per agent at a time, the timeout runs from posting and never outlasts the asker's deadline (`askRegistry.wait`), a request whose asker stops waiting is withdrawn in the chat and an answer that reaches no asker is acknowledged (`humanAnswerUnused`), while it waits the asker has lent its slot and the asked turn holds none (`turnSlot`), `/cancel` → `tools.OutcomePersonCancelled`, a request the channel could not post ends at once with `tools.OutcomePersonUnreachable` and a line from the channel's reason (`humanUnreachableError`: "Bob's chat is not set up." / "is unavailable." / "Bob's device is offline." / "Bob's chat doesn't exist." / "Couldn't reach Bob's chat."), shutdown → cancelled and withdrawn, never replayed); nothing else of the turn runs, so no model sees the conversation (no tools at all, no cogmem, empty summarization chain, no vision or transcription). The person's chat is handled before any session: `Run` → `dispatchInbound` takes text answers in arrival order (`takeHumanAnswer`, the only place an answer is taken; it clears typing/placeholder via `channels.Manager.DismissInbound` in the background), `processSessionMessage` → `handleHumanChat` does the rest (only `/` is a command there; "Nothing is waiting for your answer.", "That request has already timed out.", "That request was withdrawn.", "Please answer with text.", "Bob is not running." for a set-aside agent, whose chat is known from config). `config.HumanProblems` is the rule set (only that model; exactly one binding, its default, naming one chat no other agent is bound to; never the default agent; a human model never a default/summarization/vision/image/sub-agent model and its name never another model's): `Store.Update` refuses new violations except a missing chat, `runtimeConfig` and `claw agent` set violators aside (`PruneHumanProblems`), `GET /api/agents/human` feeds the Agents card notes and the System/Models page notes. The default agent (routing and agentreg) skips human agents; agentreg refuses cloning one or a temporary agent on a human model (`agentreg.ErrHuman`).
- **Forum** (`forum/`, design in `forum/DESIGN.md`, operator guide `docs/forum.md`):
  an agent with the per-agent `forum` switch (default off, `Config.AgentSuiteEnabled`
  case "forum", WebUI "Allow forum") gets the nine `forum_*` tools from
  `tools/forum` (suite provider; `SetService` installs the one `*forum.Service`,
  which the gateway builds in `internal/gateway/forum.go` BEFORE `NewAgentLoop`,
  since the loop builds the tools). The host side is `agent.ForumHost`
  (`agent/forum_host.go`): Messenger over the core Ask at depth
  `max_subagent_depth`-1 (the turn runs at the maximum) with the launcher as
  sender (`forum.AskInfoFromContext`), shutdown mapped to `forum.ErrShuttingDown`;
  Agents over `agentreg` (temporary participants carry `Spec.Purpose` "forum",
  `tools.TempPurposeForum`, and a clone's `CloneModel`, both persisted; `Touch`);
  the completion notice as a `system` inbound to the launcher's main conversation;
  `OnStuck` raises the `forum:<id>` alert. `ToolHost.Scope` refuses a call at
  the maximum depth (`forum.ErrForumDepth`) or from a forum participant
  (`forum.ErrForumTurn`; participants get no forum tools anyway); file
  references go through `files.Reader.Resolve`/`Allowed`. Base directory:
  `<workspace>/forums` (its file tools may read it, never write it:
  `alwaysReadableSubdirs` in `tools/files`; `forum_results` inlines each
  output up to `forum.MaxResultInlineChars`, all together up to
  `forum.MaxResultInlineTotalChars`), which the agent can still reach
  by other means, so the host trusts nothing in it: every ask is re-checked against the launcher's current
  `allow_agents` (or must reach a forum participant it owns), `Delete`/`Touch`
  name the launcher and act only on its participants, a store opened in a
  scope refuses a snapshot naming another launcher, and the notice ignores
  the recorded chat (a launch from a chat answers on the launcher's default
  binding).
  `Recover` runs once the loop has `Started()`, over every config agent (the
  switch gates only the tools); `Close` runs first in `shutdownGateway`, before
  the loop stops. A reload rebuilds the tools; running forums continue.
- **Agent message size**: `tools.MaxAgentMessageChars` (8,000) caps the
  `agent_message` tool, `/ask` and `/whisper` ("Messages to other agents are
  limited to 8,000 characters."); the core Ask/Whisper (and so the forum) are
  not capped.
- **Delivery reasons** (`channels/errors.go`): every failed send says why with a sentinel callers test with `errors.Is`, and `bus.OutboundMessage.OnDelivery` gets it: `ErrUnknownChannel` (not configured), `ErrNotRunning` (stopped, or no worker), `ErrRecipientOffline` (e.g. a paired device not connected), `ErrRecipientNotFound` (unknown chat/peer/device; Telegram "chat not found"/blocked/deactivated, Slack `channel_not_found`/`not_in_channel`/`is_archived`, Discord unknown channel/user), `ErrReceiveOnly`, else `ErrSendFailed`/`ErrTemporary`/`ErrRateLimit`. Only `ErrTemporary`/`ErrRateLimit`/unknown errors are retried. The "Channel send failed" alert fires only for not running and failed-after-retries (incl. `ErrSendFailed`); offline/not found log at WARN, receive-only at INFO. Check Up marks a human agent whose chat is on a channel that is not set up (config-derived only).
- **Built-in channels**: `channels.RegisterBuiltin(name, factory)` adds a channel every manager builds (each reload included) regardless of config; a configured channel of the same name wins. None is registered yet.
- **Agents**: named agents with separate workspaces; bindings route channels to agents.
- **Mounts** (`agents.list[].mounts`, `tools/files/mounts.go`): an external folder reached
  as `<name>/...` beside the workspace folders. The names ClawEh uses inside a workspace
  are `config.ReservedWorkspaceNames` (one list; add a folder there when code starts
  creating one): a mount with such a name (any case) is refused by `Store.Update` when a
  save introduces it, and one already in the file is set aside by `EffectiveMounts`
  (WARN at load, `GET /api/agents/mounts/ignored` marks it on the Agents page, a Check Up
  row). The automatic `maestro` mount is the only mount with a reserved name.
- **Agent registry** (`agentreg`): every agent the loop can run, with its origin.
  Config agents are built from config and rebuilt on reload (`Reload` builds the
  whole new set, then swaps it in one step with `al.cfg`). **Temporary agents**
  are created at run time (`Create`, or `CreateInTurn` which begins a turn
  atomically with insertion; options `CloneOf`, `EphemeralMemory`, `Temp(ttl)`, `OwnedBy`, and for a fresh
  agent `WithSystemPrompt`, `WithoutMemory`, `SingleShot`;
  UUID ids), kept across reloads (rebuilt; a clone always from its source's
  CURRENT config, never a stored copy; deleted when its model or clone source is
  gone; left alone while in a turn) and, unless ephemeral, across restarts
  (`internal/temp_agents.json`; any other dir under `internal/temp/` is removed
  at start), deleted by `Delete` (refused mid-turn; `BeginTurn` marks turns) or
  after `agentreg.DefaultTTL` (24h) idle by the sweep. Create/Delete take the
  registry lock only to insert/remove (builds and memory snapshots run in
  parallel with each other) and are ordered against reloads by `reloadMu`
  (Create holds it for reading from reading the config to inserting, Reload
  for writing), so a temporary agent is never kept on a superseded config;
  `Reload` reconciles with concurrent Deletes at its commit. Only the data-dir
  owner (`agent.OwnsDataDir()`, set by the gateway, which holds `claw.lock`)
  uses `internal/temp/`, restores, cleans it or writes `temp_agents.json`;
  any other process (`claw agent`) keeps its temporary agents in a private
  system-temp root removed at Close. `BeginTurn(id, inst)` refuses a stale
  instance; `runAgentLoop` retries a rebuilt temporary agent once on its
  current instance and drops the turn only if the agent is gone. They are invisible to
  operators and routing: `List`/`Default`/`ResolveRoute`/`GetConfigured` see
  config agents only (`All`/`Get` see both). A message preresolved to an agent
  that no longer exists is dropped (`errAgentGone`), never routed elsewhere; a
  clone's late async results go to its source's main conversation
  (`asyncResultTarget`, `SessionTokenStore.SetHomeResolver`).
  The registry is generic and imports no loop code: the loop hands it a
  `BuildFunc` (`AgentLoop.agentBuilder`: instance + tools), a `RetireFunc`
  (`retireAgent`: close the session, revoke tokens) and an `InsertedFunc`
  (`agentInserted`: MCP tools once visible). An agent has a **workspace**
  (prompt files, `files/`, skills, Maestro, tasks, mounts) and a **state dir**
  (`sessions/` and `cogmem/`): the same directory for a config agent,
  `internal/temp/<uuid>/` for a temporary one. Anything deriving a conversation
  or memory path uses `AgentInstance.StateDir` / `ToolDeps.EffectiveStateDir()`.
  A **clone** shares its source's workspace and config, gets a snapshot of its
  memory and its own conversation, and its tools act as the source
  (`toolIdentity`: Maestro, Fusion tokens, cron via `CronTool.SetHomeAgent`,
  task ownership) without replacing anything registered once per agent
  (`ToolDeps.TempAgent`: the source's Maestro runner is used as is, never
  re-pointed); a **fresh** temporary agent gets an empty workspace of its own
  (never seeded: no AGENTS.md/SOUL.md/IDENTITY.md/USER.md/MEMORY.md/
  COMPRESSION.md/COGMEM.md, no skills), no tools, no session token and no
  engine archive; its whole system prompt is the `WithSystemPrompt` text or
  `agentreg.DefaultSystemPrompt` (`ContextBuilder.WithFixedPrompt`), followed
  only by memory recall when it has memory. Its `agentreg.Mode` (saved with its
  prompt in `temp_agents.json`) is `memory` (default: conversation + cogmem),
  `no_memory` (conversation only, no cogmem dir) or `single_shot` (no memory;
  each turn sees only the prompt and the new message, and the conversation is
  deleted after the turn, `discardConversation`). Tools create one through
  `AgentServices.CreateFresh(model, opts...)` (`tools.WithName`,
  `WithSystemPrompt`, `WithoutMemory`, `SingleShot`). A single-shot agent's
  queued messages are never merged (each gets its own blank turn), and its
  conversation is discarded after every turn and command, even one shutdown
  interrupted. An interrupted turn of any temporary agent is not replayed after
  a restart (recovery covers config agents only; the forum resends). Inbound
  attachments are not copied into a fresh agent's workspace; the model still
  gets the refs. On a CLI model a fresh agent runs the CLI in its own empty
  workspace (never `cli/`) and never with the bypass flags, whatever
  `bypass_restrictions` says (`ProviderDispatcher.GetIsolated`); the CLI still
  applies its own built-in system prompt and its built-in non-bypass
  permission behaviour, so the exact-prompt guarantee holds for HTTP models
  only. Sub-agents (`agent_spawn`, Maestro dispatch) are ephemeral clones
  (`runSubagentTask`), deleted once their result is delivered; there is no
  sub-agent session key. Logs name a clone `alice (clone 1a2b3c4d)`
  (`AgentInstance.Label`); audit rows keep the source id in the agent column
  and add `"clone"` to details. CLI clones reach their tools through the MCP
  host's `WithAgentLookup` fallback.
- **Systemd**: `claw install` generates the unit and bakes the installer's live `PATH` into `Environment=PATH=` (target bin dir + current `PATH` + standard system dirs) — systemd does not expand `$HOME`/`~`/`%h` in `Environment=`, so paths must be absolute, which capturing the live PATH handles. The extra home-dir entries (node/pnpm/nvm, CLI-agent bins) are **not required to run ClawEh** — they are only needed to support **CLI-based providers** (claude-cli, codex-cli, antigravity-cli, cursor-cli) and tools that shell out (e.g. MCP via `npx`, skills); a core install using HTTP providers needs none of them. Re-run `claw install` if your node/nvm path changes. Set `CLAW_HOME` only for a non-default data dir (defaults to `~/.claw`); the app writes its own log to `$CLAW_HOME/logs/claw.log` — no `StandardOutput`/`StandardError` redirection needed.

## Device listener (external devices: Rabbit R1, voice apps)
Speaks the **OpenClaw Gateway WebSocket protocol** so hardware/voice clients pair and chat.
Code: `channels/device/` (protocol in `server.go`, listener/bus bridge in `gateway.go`,
read surface in `agentquery.go`); agent-loop wiring in `internal/gateway/device_query.go`.
**Full protocol + findings: `docs/device-protocol.md`.** Own listener on
`channels.device` (default port `18791`), separate from the WebUI/API HTTP (18790)
and HTTPS (18443) listeners; WebSocket with its own token/pairing auth, plain by
default or TLS (`wss://`) with `channels.device.tls`, which borrows the WebUI HTTPS
certificate from `internal/tlscert` (`injectDeviceTLS` in `internal/gateway`);
64 KiB pre-auth read limit, per-IP auth-failure lockout (behind a proxy listed in
`gateway.trusted_proxies` the IP is the `X-Real-IP` one; the WebUI listener does the
same), a Host check, a 5 s write deadline behind a per-connection send queue, and a
refusal to run on a network host with `auto_approve` or without a token. Device
tokens are stored hashed: a connect on the shared token issues fresh device
tokens and revokes the old ones. Pairing is re-checked on every request; removing a
device (WebUI) and stopping the channel (every reload) close open connections.

Status: **working** with the Rabbit R1 (through the Rabbit agent; ClawEh sees a
`mode=node` client) and the "Claw to Talk" Android app (`com.alvin.clawtotalk`,
`mode=cli`/operator) on a dev instance. Test against a dev instance only; never
against a production install.

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
  the `agent` stream first (the OpenClaw Gateway's stream-then-finalize order) makes it speak + display.
- **Partial streaming (opt-in per channel).** Streaming-capable channels (`StreamCapable`, the
  device listener) get partial text as the model generates: coalesced `chat:delta` + `agent:assistant`
  deltas, each with an **incrementing per-run payload `seq`** (reused seq → clients drop it as a
  duplicate; that's why the R1 once spoke only the first chunk). Streamed `agent:assistant`
  `data.text` carries the **increment**, not the running total. `emitChatReply` skips the full-text
  `agent:assistant` event for a streamed run (deltas already sent it) — avoids double-speak.
  `streamToolNarration` (`stream_coalescer.go`, default on) is the off switch. spawnllm HTTP
  providers stream via SSE; CLI providers return the whole reply (no deltas).
- **Auth:** a long 32-byte token (in the QR, for the R1) OR a typeable 5-word BIP39
  `word_token` passphrase (for apps), both constant-time; plus per-device Ed25519 pairing
  approval (cryptographic — locks to that install). Removing a paired device revokes its tokens
  and closes its connections. Pending pairings: at most 20, each expires after 10 minutes.
- **Agent selection / session scope:** the client encodes the selected agent as the session
  key's 2nd segment (`agent:<id>:<peer>:<profile>`); node clients send the `main` sentinel and
  use their per-device assignment (else the default agent). Every device joins the selected
  agent's main conversation (`agent:<id>:main`), whatever the rest of the key says — one
  agent, one history, one memory across the R1, the app, Slack, Telegram, and MCP service
  tokens. There are no session modes; an unknown agent id is refused. `chat.history` resolves
  through the same rule as `chat.send` (`Server.sessionScopeKeyFor`). Every other inbound path
  goes through `routing.ResolveAgentSessionKey`, which keeps only the agent's own sub-agent
  key and collapses anything else to `agent:<id>:main`: cogmem's inbox is fed from exactly
  one persistent session per agent. Mechanism: `metadata["session_key"]` +
  `metadata["preresolved_agent_id"]`. `agents.list` falls back to the id as the display name
  (clients hide name-less agents).
  **Isolation is a property of the agent** — a separate agent (optionally `cogmem: false`), not
  a separate channel.
- **`/agent` command (node clients):** node clients (R1) switch assistants by typing `/agent`
  (list), `/agent <name-or-id>` (switch), or `/agent default` (reset). `handleChatSend`
  intercepts it and persists to `paired_devices.agent_id` via `SetDeviceAgent` — the same field
  `sessionScopeKeyFor` reads, so it survives restarts. Reply goes through the normal event path.
- No permessage-deflate (disabled end-to-end); the OpenClaw `agent` event schema is
  `{runId, seq, stream, ts, data}` with no top-level `status` (clients default it to "unknown").

## Testing — always keep tests in sync (do not skip this)
- A change is not done until its tests are updated AND passing. Run `make test` before calling anything done — it is the whole suite, lint included, and the thing a pipeline would gate on. `go test ./...` is the fast Go-only loop for iterating.
- **Add tests for new behavior.** New config flags, gating, and branches need a test for both the on and off paths — not just a tweak that makes existing tests compile.
- **Keep test fixtures in sync with renames/refactors.** When tool names, config keys, or APIs change, grep the whole repo (including `*_test.go`, `test.sh`, `tests/`) and update every reference. A rename that compiles can still break integration tests.
- **MCP integration tests are part of the suite.** `make test` runs `test.sh`, which runs `tests/test_mcpserver.sh` via the external `probe` binary against an ephemeral ClawEh instance. Every provider tool must be exposed in the test config and probed: success for hermetic tools, graceful-error probes for network/LLM tools (web, skill, agent_spawn). Add a probe case when you add a tool.
- After implementing, do a final grep for the old name/symbol to confirm nothing stale remains in code, tests, scripts, or docs.

### Capability changes — the golden fixture and the BREAKING entry

What an existing configuration lets each agent do is pinned by
`tests/capabilities`: a production-shaped fixture (`testdata/fixture.json`, one
Fusion service under `testdata/fusion/`) resolved into `testdata/effective.golden`,
per agent (native tools, suites, the Fusion tools the real engine registers,
MCP grants) and per CLI model (the exact command line and environment). It
exists because two changes passed every unit test and still cost agents their
tools in production: the CLI bypass flag becoming opt-in, and per-service Fusion
gating disappearing when MCPFusion was folded in.

- Any change under tool gating, allowlists (`tools`, `mcp_tools`,
  `deny_tools`), suite switches (`fusion`, `maestro`, `cogmem`), provider or
  CLI arguments and environment, or their defaults, changes that golden file.
  Regenerate it in the same commit (`UPDATE_GOLDEN=1 go test
  ./tests/capabilities/`), read the diff, and add a **BREAKING** changelog
  entry that names the step restoring the previous access. Never regenerate it
  to make the gate pass without doing both.
- A default that changes what an existing install can do must also show on the
  Check Up page and in the reply the user gets when it bites, so the operator
  learns of it from the running system, not from a failed job. It is not an
  alert: alerts are for outages, and a configuration state is not one.
- The MCP integration test (`tests/test_mcpserver.sh`, section 8) checks the
  Fusion gating on the running binary; the opt-in CLI smoke test
  (`CLAW_TEST_CLI=1`, `tests/test_cli_provider.sh`) exercises a real CLI's
  permission path. Extend them when a new kind of grant appears.

### WebUI regression suite — run it for any significant frontend change

`make test` does not load a page. The browser suite is a separate target
because it needs a live instance to drive, and it must be run for any
significant change to `web/frontend`, to the `/api/*` handlers behind it, or to
anything that alters ClawEh startup, readiness or config reload:

```
make build                       # then install build/claw into the dev instance and restart it
until curl -sf http://127.0.0.1:<dev-port>/ready >/dev/null; do sleep 1; done
export CLAW_E2E_USER=<admin> CLAW_E2E_PASSWORD=<password>   # the dev instance's `claw admin` account
make check-webui
```

The dev instance's service name, port and install path are machine-specific
and belong in `CLAUDE.local.md`. Stop the service before copying the binary
over it: a running binary makes the copy fail with "Text file busy", and a
restart then silently brings the old build back.

The suite logs in first (the WebUI and `/api/*` require the admin account), so
the dev instance needs one (`claw admin`) and the two variables must be
exported; it exits 2 with a hint otherwise.

- **The plan is `docs/webui-test-plan.md`** — 146 numbered checks, each with a
  process and an expected result, followable by hand. `tests/frontend-e2e.mjs`
  executes it and prints the same step IDs. Keep the two in step: a step added
  to one belongs in the other.
- **Dev only.** Groups F, G, N and S write (F creates an agent, G edits a config
  field, N creates a memory domain and curates inside it, S5 creates a
  throwaway agent and toggles one of its tools). All four revert what they
  change. The runner refuses port 18790 unless `--allow-prod` is given.
  Never point it at production.
- **Wait for `/ready`, not `/health`.** `/health` answers as soon as the port
  is open; `/ready` waits for the channels. Starting early makes steps fail for
  no reason.
- **When a step fails, decide first whether the PLAN or the PRODUCT is wrong.**
  Both happen. It has caught real defects (`/ready` stuck at 503 forever, an
  unlabelled delete button) and has itself been wrong (sidebar entries are
  disclosure controls, not links; the agent rail labels by `name || id`).
  Fixing a correct test to match broken behaviour is the one outcome to avoid.
- **If you are unsure whether a change is significant enough to warrant a run,
  ask.** It takes a couple of minutes; a silent WebUI regression does not
  announce itself.
- Ordinary Go-only changes do not need it. `make test` already runs the
  frontend typecheck, oxlint and vitest, none of which load a page.

## Workflow Rules
- Work on a fix or feature branch (`fix/...`, `feature/...`), and commit and
  push to it at logical points (a tested, self-contained step), without waiting
  to be asked.
- Never push directly to main — use feature branches + PRs.
- **Never create, move, or delete git tags.** Tags are cut by the release build
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
  `version = "..."` line.
- Always compile after edits before declaring done: `go build ./...` for Go changes, and `cd web/frontend && pnpm run build:backend` for frontend/TypeScript changes. The frontend bundle lands in `web/backend/dist`, which is embedded by `web/backend/embed.go` into the merged claw binary.
- When investigating a problem, report findings and wait for approval before implementing.
- Use Alice and Bob as example agent names in all docs and examples.
