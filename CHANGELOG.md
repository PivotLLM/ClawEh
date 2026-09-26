# Changelog

All notable changes to ClawEh are recorded here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and
version numbers follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Entries describe what changed for someone **running or integrating with** ClawEh
— config keys, tool names, API shapes, protocol behaviour, defaults — not the
internal refactors behind them. A change nobody outside the repository can
observe does not need an entry.

## [0.6.0]

### Security

- **BREAKING: the WebUI and its HTTP API now require a login.** There is no
  default account and no way to create one from the browser: run `claw admin`
  on the server. It asks for a username and a password (twice, no echo, at
  least 12 characters) and writes `<CLAW_HOME>/credentials.json` (mode 0600,
  argon2id). Until that file exists the WebUI shows "No admin account. On the
  server run: claw admin" and every `/api/*` request answers 401
  `{"error":"no admin account","hint":"run: claw admin"}`; channels keep
  working. A running gateway notices the file within a minute and signs
  everyone out when it changes. Sessions are cookies (`claw_session`, or
  `__Host-claw_session` over HTTPS), idle 12 h, absolute 7 days, kept in memory
  (a restart signs everyone out). Five failed logins from one address lock it
  for 1 minute, doubling to 1 hour; 100 failures in 10 minutes from anywhere
  lock all logins for 60 seconds; the first lockout per address raises the
  "WebUI login locked out" alert. Exempt from login: `/health`, `/ready`,
  `/ping`, the MCPFusion OAuth API under `/api/v1/`, `POST /api/message/{token}`,
  the signed LINE webhook, and the login endpoints themselves. Loopback is
  **not** exempt. A credentials file readable by group or others is ignored
  (logged with the `chmod 600` fix) and counts as no account. See
  `docs/webui-auth.md`.
- **BREAKING: the gateway no longer serves plain HTTP off-box.** Plain HTTP is
  served on loopback only (`127.0.0.1:18790` and `[::1]:18790`). When
  `gateway.host` is not a loopback address the gateway additionally serves
  HTTPS on `gateway.host:gateway.tls_port` (new key, default `18443`); there is
  no opt-out. Installs with a non-loopback `gateway.host` move from
  `http://<host>:18790` to `https://<host>:18443`: browse to the new URL and
  accept the self-signed certificate (verify its fingerprint with `claw tls`),
  or install your own with `gateway.tls.cert_file` / `gateway.tls.key_file`.
  Reverse-proxy users point the proxy at `http://127.0.0.1:18790` and set
  `gateway.external_url`. `gateway.external_url` now defaults to
  `https://<hostname>:<tls_port>` when the HTTPS listener is on. New
  `gateway.tls` block: `cert_file` + `key_file` (PEM, both or neither — one
  alone is a config error) and `extra_names` (additional DNS names / IPs for
  the self-signed certificate). Without a pair, a self-signed ECDSA P-256
  certificate is generated in `<CLAW_HOME>/tls/` (key 0600), valid one year,
  for the host name, its FQDN, every non-loopback interface address, the host
  of `external_url` and `extra_names`; it is regenerated when under 30 days
  from expiry or when those names change. Both sources are hot-reloaded from
  disk within a minute; a pair that fails to load keeps the previous
  certificate serving and raises an alert. TLS 1.2 minimum. HSTS is sent only
  with an operator-supplied certificate. New `claw tls` command prints the
  certificate in use (source, names, expiry, SHA-256 fingerprint);
  `claw tls --regenerate` replaces the self-signed pair. `mcp_host.listen` must
  be a loopback address; the gateway refuses to start otherwise. See
  `docs/tls.md`.
- **BREAKING:** the shared HTTP listener (WebUI, `/api/*`, `/webui/ws`,
  `/health`, `/ready`, channel webhooks) now answers only to known host names
  and rejects everything else with `421 Misdirected Request`. Allowed are
  `localhost`, `127.0.0.1`, `::1`, the `gateway.host` bind address, the
  certificate's names and the host of the advertised external URL
  (`gateway.external_url` when set; otherwise the bind address, or the primary
  LAN IP for a `0.0.0.0` bind). This stops DNS-rebinding attacks that reach a
  loopback listener through an attacker-controlled name. **Migration:** if you
  reach ClawEh through any other hostname or IP (a reverse proxy name, a second
  interface, an `/etc/hosts` alias, a monitoring probe by hostname), set
  `gateway.external_url` to that URL and reload; the change takes effect
  without a restart. The device gateway listener applies the same check (IP
  literals are always accepted there, since the pairing QR advertises them).
- Cross-site request forgery protection on the shared listener: state-changing
  requests (POST/PUT/PATCH/DELETE) that a browser marks as coming from another
  site are rejected with `403`, so a web page open in the same browser can no
  longer drive `/api/*`. Same-origin requests, non-browser clients (curl,
  scripts, claw-auth) and the origin of `gateway.external_url` are allowed. The
  signed LINE webhook and the token-gated `POST /api/message/{token}` are
  exempt because they authenticate each request themselves. Every response now
  carries `Content-Security-Policy: frame-ancestors 'none'`,
  `X-Content-Type-Options: nosniff` and `Referrer-Policy: no-referrer`;
  `/api/*` responses are `Cache-Control: no-store`.
- **BREAKING:** `GET /api/webui/token` and `POST /api/webui/token` are removed,
  and with them `channels.webui.allow_token_query` and
  `channels.webui.allow_origins` — delete both keys from `config.json`. The
  browser now opens `/webui/ws` with its login session cookie; the WebSocket
  origin check is always same-origin. The WebUI channel token remains only for
  non-browser clients (`Authorization: Bearer <token>` or the `claw-token`
  subprotocol) and is never accepted from the URL. `POST /api/webui/setup` no
  longer returns `token` or `ws_url`.
- **Device gateway hardening.** The device listener now enforces the payload
  limits it advertises: an unauthenticated connection may send at most 64 KiB
  before the handshake completes, and 25 MiB (`maxPayload`) after it; a larger
  frame closes the connection. Repeated failed authentications from one client
  address (5 within 10 minutes) lock that address out — `connect` is answered
  `AUTH_RATE_LIMITED` with `retryAfterMs` — for 1 minute, doubling on each
  further lockout up to 1 hour; the first lockout for an address raises the
  "Device authentication locked out" alert. At most 32 connections may sit in
  the handshake at once; further upgrades get 503 until one finishes.
  Thresholds are in `channels/tuning.go`. An inbound WebUI socket message is
  capped at 1 MiB.
- Device gateway: hello-ok now echoes the device token the device connected
  with; connecting on the shared `token`/`word_token` (including the first
  connect after approval) issues fresh device tokens and revokes the device's
  previous ones.
- Service tokens (`state/service-tokens.json`) and device-gateway tokens
  (`gateway.db`) are now stored as SHA-256 hashes instead of plaintext; a
  presented token is hashed for lookup. **On first start after upgrading, both
  stores are rewritten in place once** (the JSON file atomically at 0600, the
  SQLite rows in one transaction); existing issued tokens keep working, and
  `claw token list`/the report are unaffected. Named message-API tokens stay
  readable in the WebUI by design and their store is forced to 0600 on load.
- `GET /api/config` now masks search-provider `api_keys` lists, every MCP
  server `env` and `headers` value, every CLI model `env` value, and the
  credentials embedded in `proxy` URLs (`scheme://****@host`);
  `GET /api/providers` masks proxy credentials too. Masked values are never
  shown as more than 7 characters, and values shorter than 12 are hidden
  entirely. PUT/PATCH `/api/config` and `PUT /api/providers/{index}` restore
  masked values from disk, so a read-edit-write round trip cannot overwrite a
  secret with its mask.
- The gateway now enforces data-directory permissions at startup: `CLAW_HOME`
  is made `0700`, and every database (`*.db`, `*.db-wal`, `*.db-shm`,
  `*.sqlite*`), `credentials.json`, `state/*.json`, `tokens/*`, `tls/*.key` and
  any file whose name contains `token` or `secret` under it is tightened to
  owner-only, with each change logged. Symlinks are left alone and the
  `media/` and `logs/` trees are not scanned. **Startup now refuses to run when
  `config.json` is readable by other users** (any group/other permission bit),
  since it holds provider keys and tokens. The error names the fix:
  `chmod 600 <path>`. Installs whose config was created by ClawEh are already
  `0600`; a config copied or edited by hand may need the command once. The
  device pairing database (`state/gateway.db`) and the Fusion OAuth token store
  (`state/fusion-tokens.db`) are now created `0600` from the first write,
  including their SQLite `-wal`/`-shm` side files. Directories and files
  ClawEh creates under `CLAW_HOME` (agent workspaces, logs, dumps, sub-agent
  task files, skills, common files) are now created owner-only (0700/0600)
  instead of world-readable.
- Child processes no longer inherit the service environment. `shell_exec`
  commands and stdio MCP servers now start from an allowlisted environment
  (PATH, HOME, USER, LOGNAME, SHELL, LANG, LC_*, TERM, TMPDIR, TZ, XDG_*,
  SSL_CERT_FILE/SSL_CERT_DIR, proxy variables, and the Node/nvm/npm variables
  `npx`-based servers need); everything else, in particular `CLAW_*`
  configuration and `ALERTER_*` credentials, stays in the gateway process. An
  MCP server that relied on a variable inherited from the service (an API
  token, for example) must now receive it through that server's `env` or
  `env_file`. CLI providers (claude-cli, codex-cli, antigravity-cli,
  cursor-cli) receive the same allowlist plus their own login and API-key
  variables (`ANTHROPIC_*`, `OPENAI_*`, `GOOGLE_*`, `GEMINI_*`, `CLAUDE_*`,
  `CODEX_*`, `CURSOR_*`, `AGY_*`, `ANTIGRAVITY_*`).
- `web_fetch`, `web_search`, and every external MCP tool now hand their output
  to the model wrapped in untrusted-content markers: a one-line notice that the
  text is data, not instructions, then `<<<UNTRUSTED_CONTENT id=…>>>` …
  `<<<END_UNTRUSTED_CONTENT id=…>>>` with a random per-call id so retrieved
  text cannot forge the end of the block. Model control tokens inside the
  content (`<|im_start|>`, `<|endoftext|>`, `[INST]`, `<<SYS>>`, any `<|…|>`
  token, and the markers themselves) are replaced with `[removed-token]`. What
  is shown to the user is unchanged; the default AGENTS.md template explains
  the markers.
- `web_fetch` SSRF guard now also blocks 192.0.0.0/24, 198.18.0.0/15,
  240.0.0.0/4, and the NAT64 prefix 64:ff9b::/96 (including IPv4-mapped forms).
  When `proxy` is configured, the target hostname is resolved and checked
  before the request is sent (and on each redirect), since the connect-time
  guard only sees the proxy address in that mode.
- The systemd unit written by `claw install` (system mode) and the shipped
  `claw.service` now set `NoNewPrivileges=yes` and `PrivateTmp=yes`; the
  user-mode unit sets `NoNewPrivileges=yes`. Existing installs pick this up by
  running `claw install` again. Under `NoNewPrivileges`, commands the agent
  runs cannot escalate via `sudo` or setuid binaries.
- **BREAKING (release process):** `claw upgrade` now verifies releases with a
  publisher signature. Every release must ship `checksums.txt` and
  `checksums.txt.minisig` (minisign, signed with the key embedded in the
  binary); a release without them, or with a signature from another key, is
  refused. The per-archive `.sha256` files are no longer consulted by
  `claw upgrade` (they are still produced for the install scripts). A build
  with no embedded key refuses to upgrade at all. Release maintainers:
  `make release-sign MINISIGN_KEY=...`, see `internal/upgrade/pubkey.go`.
  `make test` now runs `govulncheck` and fails on a known vulnerability
  reachable from the code.


### Added

- **Configuration report.** A new Report page (after Services in the WebUI
  menu) opens a PDF, the ClawEh Configuration Report, describing what this
  install can do: identity and the user it runs as, a security assessment
  table with a mark on each item where action is recommended (HTTPS and
  operator authentication are not implemented yet and are flagged when a
  listener is reachable from other hosts), a summary of what Claw can access,
  every listener, providers and models (CLI providers with the exact command
  line they are launched with), credentials as set or not set, channels and
  who may use them, each agent's tools, MCP access and every folder it can
  read or write, external services, devices, data at rest and scheduled
  activity. Endpoint `GET /api/report/pdf`. Secret values never appear. See
  `docs/report.md`.

- **Operator alerts.** Conditions the operator should hear about are written
  to `<CLAW_HOME>/logs/alerts.log` (or the file named by `ALERTER_LOG`), one
  record per alert with its priority: a model parked for an
  authentication or billing failure (a CLI logged out, a key revoked), a
  model parked after repeated failures, an unreachable MCP server, a channel
  that failed to start, could not deliver a message, had its token rejected
  (Telegram, Slack, Matrix), has had no working connection for ten minutes
  despite retrying (Telegram, Slack, Discord, Matrix, SecMsg; the threshold is
  `ConnDownAlertAfter` in `channels/tuning.go`) or whose device gateway
  listener stopped, SecMsg with no accounts, a scheduled job that failed or could not be delivered, an
  unreadable or unwritable cron store, a session that could not be saved,
  service tokens that could not be loaded, an invalid config edit or a failed
  reload, a failed nightly backup or log rotation, and the WebUI/API listener,
  MCP host server or agent loop stopping; also the Fusion token store,
  Maestro setup, cognitive-memory migration, message-token stores, session
  state, sub-agent records, mount watching, voice transcription credentials
  and device sources. Repeats of the same alert within
  ten minutes are counted, not repeated. The Logs page shows the alerts log
  through its new source selector, and `GET /api/gateway/alerts` returns it.
  Every alert is listed in `ALERTS.md`; the record format is in
  `docs/alerts.md`. Alerts are also delivered to any channel configured
  through `ALERTER_*` environment variables or `~/.alerter` (Pushover, SMS,
  SMTP mail, webhook). All ClawEh alerts are normal priority.

- `claw admin [username]`: create or replace the WebUI admin account. Writes
  the credentials file in the service's `CLAW_HOME` (the `CLAW_HOME` variable,
  else the installed unit's, else `~/.claw`) and, when run as root, hands it to
  the service account. New endpoints `GET /api/auth/status`,
  `POST /api/auth/login`, `POST /api/auth/logout`.
- **Audit log.** ClawEh now keeps an append-only record of who did what in
  `<CLAW_HOME>/audit.db` (SQLite, mode 0600): every agent tool call (agent,
  session, channel, sender, tool, redacted argument digest, outcome,
  duration), every configuration save through the WebUI (operator, client IP,
  and which top-level config sections changed — never the values), and WebUI
  login/logout/lockout events. Rows are kept for 90 days. Read it in the new
  **Audit** page (Services → Audit) or via
  `GET /api/audit?since=&until=&kind=&agent=&session=&limit=&before_id=`.
  Recording never blocks a turn: if the write queue is full the event is
  dropped and counted, and the page shows the count. Every agent turn also
  gets a short random `turn_id` (8 hex) carried on its inbound, routing,
  tool-dispatch and outbound log lines and on its audit rows, so one turn's
  activity can be pulled together across `claw.log` and the audit log. See
  `docs/audit.md`.
- **Full backup and restore.** The nightly backup now writes one archive,
  `claw-backup-<timestamp>.tar.gz` (0600, in a 0700 directory), containing
  `config.json`, the cron jobs file, `state/` (service and integration tokens,
  the device pairing database, the fusion OAuth token store),
  `credentials.json` and `tls/` when present, and every SQLite database under
  `CLAW_HOME` — session archives and cognitive memory included. Databases are
  checked with `PRAGMA quick_check` and copied with SQLite's `VACUUM INTO`, so
  a live store is captured consistently, WAL included; a database that fails
  the check is left out and raises the alert "Database failed integrity check"
  while the rest of the backup completes. Media caches, logs and per-agent
  `tmp/` are excluded. New config key `backup.dest` chooses the destination
  directory (an off-host mount, for example); `retain_days` now prunes
  archives and the old `YYYYMMDD` folders alike. New commands:
  `claw backup [--dest DIR]` runs the same backup on demand and prints what was
  written; `claw restore <archive> [--yes]` restores one, refusing while the
  gateway runs, listing every file it will replace, moving the current files to
  `restore-backup-<timestamp>/` and aborting before any change if a restored
  database fails its integrity check. `POST /api/backup` additionally returns
  `archive`, `bytes` and `skipped`. See `docs/backup.md`.
- `agents.list[].deny_tools`: a per-agent list of tools the agent may never
  call, evaluated after every grant and always winning — over `tools`, over
  `mcp_tools`, and over the suite toggles (`fusion`, `maestro`, `cogmem`, the
  discovery meta tools). Internal and suite tools match by case-insensitive
  name or a `*`-suffixed prefix (`shell_exec`, `google_calendar_event_delete`,
  `google_drive_*`); MCP-client tools match `<server>_<tool>` by equality or
  prefix without the `mcp_` prefix (`google_drive_file_share`,
  `google_calendar`). A denied tool is neither advertised to the model nor
  executable, for interactive, cron and sub-agent turns alike. Editable on the
  Agents page ("Denied tools") and listed in the configuration report.
- **Inbound flood control.** Messages that arrive while a session is already
  answering are no longer each given their own turn. They queue, and when the
  running turn finishes all queued messages from the same chat run as one
  turn, their texts joined in arrival order (newline-separated) with the reply
  addressed to the last of them; messages from different chats that share a
  session are never merged, and a command always runs on its own. New
  `agents.defaults.max_concurrent_turns` (default 8; `0` = unlimited, read at
  startup) caps how many turns run at once across all sessions; further turns
  wait for a free slot.
- **Daily spend alert.** New `agents.defaults.daily_spend_alert_usd` (default
  `0` = off): the first time the day's (UTC) summed model cost, as reported by
  the providers, reaches this amount an operator alert is raised (once per
  day). Sub-agent worker turns and compaction calls are not counted.
- CI workflow (`.github/workflows/ci.yml`: `make test` on Ubuntu, build and
  test on macOS), `make sbom` (CycloneDX `build/sbom.json`),
  `make release-checksums` and `make release-sign`.

- **Secret references in `config.json`.** Any credential field (`api_key`,
  `*_token`, `*_secret`, `*password`, `api_keys` entries, MCP/CLI `env` and
  `headers` values, `proxy` URLs) may be written as `env:NAME` to read an
  environment variable or `file:/absolute/path` to read a private (0600) file.
  The value is resolved when the config loads and the reference is written back
  on every save, so the WebUI can edit the config without the secret ever
  landing in `config.json`. `GET /api/config` shows the reference as written. A
  missing variable, an unreadable file or a file readable by group/other is a
  load error naming the config key and the `chmod 600` fix.
- `session.retention_days` (default `0`, keep forever): a nightly job (03:45
  local) deletes any session archive whose last activity is older than that
  many days. An agent's `main` and `service` sessions, sessions with a turn
  pending, and sessions the running loop still holds open are never deleted;
  `archive_days` remains the per-message trim for the shared session. The same
  job removes cogmem pre-migration snapshots (`cogmem.db.pre-vN.db`) older than
  30 days. Failures raise the "Session retention failed" alert.
- `claw sessions erase --channel <ch> --chat <id> [--all]` and
  `DELETE /api/sessions?channel=&chat_id=[&all=true]` (login required) delete
  every session belonging to one sender on one channel across all agents and
  print exactly what was erased. Under the default `unified` scope a sender's
  messages live in the agent's shared `main` session, which has no per-sender
  column: it is reported and only deleted with `--all`. Cognitive memories are
  not touched, because cogmem records no per-sender provenance. The CLI refuses
  to run while the gateway is up; use the API then.
- Config page → Backup: a **Destination directory** field for `backup.dest`
  (blank = `<CLAW_HOME>/backup`). Saving from the Config page previously
  dropped an existing `backup.dest`.

- The Report page now shows the security assessment inline: a product
  identification line (name, version, build, platform), the assessment table
  with rows needing action marked, and a **Download full report** button for
  the PDF. New endpoint `GET /api/report/assessment` returns the identity and
  the assessment rows as JSON
  (`{"identity":{name,version,build,platform,generated_at},"assessment":[{action,item,status}]}`)
  — the same rows the PDF renders, never a secret value, behind the same login
  as the rest of `/api/`.

### Changed

- **Fix for MacOS.** Fixed two tools/maestro tests that failed on macOS because they compared raw t.TempDir() paths against symlink-resolved roots (/var vs /private/var); the import gate itself was correct. test.sh now re-prints failing Go test output, lists each failed Go test and MCP integration check by name in the final summary with rerun commands, and saves details to .test-failures.log; a startup-template check that could not fail the run now does.
- **Colour fix.** Fix colour on text produced by test.sh and
  tests/test_mcpserver.sh: the scripts printed the escape codes literally
  (`\033[...`), on macOS and Linux alike. Colours are now off when output is
  not a terminal or `NO_COLOR` is set, and `./test.sh -n` also silences the
  integration sub-script.

- **golangci-lint is back in the `make test` gate.** The remaining findings
  are fixed: the agent test fixture returns a struct instead of five values,
  duplicated tests are table-driven, the unused YAML round-trip test and the
  three stray `yaml` struct tags on `ModelConfig` are gone (config is JSON
  only), `Config` and `AgentDefaults` use pointer receivers throughout, and the
  MCP manager test proves liveness with a real request instead of the retired
  `ping` RPC.

- **Errors that used to be swallowed are now reported.** With errcheck in
  the gate, every ignored error return is handled. Most of that is invisible
  (debug-level logs on closing read-only handles), but a few tool and API
  results change: a malformed `after_line`, `at_offset` or `start` in the
  file range-edit tools is an error instead of silently 0; the cron add tool
  reports a failed job update; the memory list API returns 500 when the
  store fails instead of an empty list; device-store failures surface as
  errors rather than "not found"; `claw status` shows "Cognitive memory
  unavailable: <error>" instead of zero counts. gosec also runs, with the
  intentional file modes and test files excluded by config, and adds a
  read-header timeout to the device gateway, MCP host and OAuth callback
  servers.

- **MCP liveness probe on by default.** `tools.mcp.liveness_probe_seconds`
  now defaults to 60 (was 0, off). Every connected external MCP server is
  asked for its tool list once a minute; a failed probe reconnects it, and a
  changed answer refreshes its tools within the interval. Set the key to `0`
  in `config.json` to restore the old behaviour; an explicit `0` is kept on
  save.
- **Contexts are threaded through instead of started fresh.** Progress
  placeholder edits, stream deltas, tool breadcrumbs and fallback notices are
  now bound to the turn they belong to, so a cancelled or timed-out turn no
  longer keeps publishing after it ends. WebUI memory handlers stop when the
  client disconnects. Work that must outlive its trigger (sub-agent callbacks,
  idle eviction, reload, graceful shutdown) is explicitly detached.

- **Legacy code inherited from the original fork is replaced or removed.**
  The standalone sub-agent tool loop, the retired launcher's config shim, the
  unreferenced upstream assets and the per-file upstream copyright headers
  are gone; the default-model provider constructor and the core logger file
  are renamed to say what they do; `--version` names only Tenebris
  Technologies. The original MIT notice stays in `LICENSE`, and the project's
  origins are recorded in `docs/HISTORY.md`.

- **MCP access is a checkbox list.** The agent card shows one checkbox per
  configured MCP server instead of a comma-separated text field: checked
  grants every tool the server publishes. An entry that names no configured
  server (a server since removed, or a hand-typed partial grant from before)
  stays visible, checked and flagged, so it can be removed; the WebUI no
  longer offers finer-than-server grants. The saved `mcp_tools` list is
  unchanged in shape. The card is
  regrouped into Skills, Tools (MCP access first, then the native tool list,
  now titled "Internal tools" rather than "Always-On Tools") and Mounts.

- **BREAKING:** CLI providers no longer pass skip-permissions /
  sandbox-bypass flags by default; tick *Bypass CLI restrictions* on the CLI
  (or set `bypass_restrictions: true` on its provider) to restore the previous
  behaviour. A bypass flag left in a model's `extra_args` is ignored (with a
  warning) unless the provider setting is on; with it off, a CLI that refuses a
  tool call now returns a clear error naming the setting instead of an empty
  reply, and that message survives a failed model fallback chain.
  `GET /api/system/clis` reports `bypass_args` and `bypass_restrictions`;
  `PUT /api/system/clis/{protocol}` accepts `bypass_restrictions`. The
  configuration report shows the setting per CLI provider and lists each CLI
  with it on in the security assessment.
- **`/cancel` stops the running request.** It now cancels the turn in progress
  for the session as well as dropping the messages queued behind it, without
  waiting for the turn to finish. The reply says which it did: "Cancelled the
  current request and N pending message(s).", "Cancelled the current
  request.", "Cancelled N pending message(s).", or "No pending messages to
  cancel."; the interrupted turn replies "⚠️ Cancelled by /cancel. Some steps
  may have completed — ask me to continue if needed." rather than a time-limit
  message. Messages sent after the `/cancel` are answered normally.
- Tool results are capped before they enter the model's context: one result
  may occupy at most 25% of the model's context window (4 chars/token, floor
  16 KiB, ceiling 512 KiB). The head is kept and a marker
  `[output truncated: kept N of M characters. Use the tool's paging/range
  options or a narrower query to see more.]` is appended; error text is never
  cut below 4 KiB. The cap also applies to async sub-agent (`agent_spawn`)
  results that arrive later as a system message. Previously a multi-megabyte
  tool result could not be compacted away and made the turn fail after
  repeated `max_tokens` halving.
- Context-overflow and timeout detection for the LLM retry loop now uses the
  shared spawnllm error classifier, so an HTTP 413 or "payload too large" also
  triggers history compression, and transient 5xx/parse failures are retried
  with backoff instead of failing the turn immediately. Retry backoffs (LLM
  timeout retries, channel start retries, outbound send retries) now carry
  ±20% jitter so concurrent retries do not hit a provider in lockstep.
- Inbound messages redelivered by a platform (Slack event retries, repeated
  updates) are dropped when the same chat + message id was seen in the last 30
  minutes (1024 most recent per channel), so a redelivery no longer runs the
  message twice. Messages without a platform id are never deduplicated.
- Per-session dispatch state is released after an hour idle instead of being
  kept for the life of the process.
- Binaries are built with `-trimpath`; setting `SOURCE_DATE_EPOCH` makes a
  rebuild of the same commit bit-identical.
- Configuration report: the security assessment table gains rows for data
  directory permissions (files under `CLAW_HOME` readable by other users, with
  the first offender and the chmod fix), device auto-approve
  (`channels.device.auto_approve`), the HTTPS certificate (self-signed
  fingerprint to verify with `claw tls`, or a user certificate expiring within
  14 days), the audit log (`<CLAW_HOME>/audit.db`, 90-day retention, flagged
  when missing), a per-agent reminder that `shell_exec` is not confined by
  `restrict_to_workspace`, and the "Operator authentication" row now reports
  whether an admin account exists. The Network section lists both gateway
  listeners and the certificate.

- The gateway and the WebUI API now share one in-memory configuration. API
  handlers read the running config instead of re-parsing `config.json` on every
  request, and every save takes a lock, so two concurrent saves can no longer
  overwrite each other. Saves through the API are validated with the same
  listener checks the gateway applies at startup (a certificate without its
  key, or `mcp_host.listen` off loopback, is rejected with 400 instead of being
  written and refused on the next start). Unknown keys in `config.json` are now
  reported at startup as `unknown config key: <path>` (a typo previously took
  effect silently); loading still succeeds.
- The gateway now exits with status 3 after a clean shutdown when a core
  service dies after startup (the HTTP listener on any of its addresses, the
  MCP host server, or the agent loop), instead of staying up half-dead;
  systemd's `Restart=on-failure` restarts it. The existing "HTTP listener
  stopped", "MCP host server stopped" and "Agent loop stopped" alerts are still
  raised first. A shutdown that hangs is cut off after 20 s.
- Every request body on the gateway listener is capped: 1 MiB by default (413
  when Content-Length exceeds it), 32 MiB under `/api/memory/` (memory import)
  and 4 MiB for `POST /api/skills/import`.
- A session-store write failure (user message, assistant reply, tool call or
  result) now fails the turn with a clear error and raises the "Session store
  write failed" alert, instead of continuing on a history the store did not
  accept. Context compaction raises "Context compaction breaker tripped" when
  three automatic compactions fail in a row.
- Context handling (ctxengine): when the automatic-compaction breaker was
  tripped, the emergency pass reported success without running, so an
  oversized request could reach the provider — the safety net now bypasses the
  breaker. A restart between an assistant's tool calls and their results no
  longer hides that the tools ran: the unanswered calls get an "interrupted —
  outcome unknown" result so the model does not re-run them. Compaction writes
  the new window, summary and checkpoint in one SQLite transaction. Conversation
  summaries can no longer carry instructions: tool output is delimited before
  summarization, quoted instructions are accepted only from user messages, and
  the summary is rendered as a marked data block at the end of the system
  prompt. Token estimates carry a 15% safety margin and calibrate themselves
  from the token counts providers report. Eviction runs in batches and per-turn
  memory injections are appended as a trailing message, so the cached prompt
  prefix survives between turns; eviction placeholders name the archive message
  number and the `session_messages` tool. The session archive keeps up to 256 KB
  of each tool result (was 4 KB).
- CLI providers (claude-cli, codex-cli, antigravity-cli, cursor-cli) now run as
  a process group: a timeout or cancel terminates the CLI and every process it
  spawned (MCP servers, shells), and a lingering pipe can no longer hold the
  turn past the timeout (+5 s). Captured CLI output is capped at 64 MiB.

- `DELETE /api/models/{index}` now answers 409 Conflict while any agent model
  list, `agents.defaults` chain (models, image, vision), `summarization.models`
  or `subagents.models` still references the model; the body names every
  referencing site, and the WebUI delete dialog shows it. Repoint them first,
  then delete. Saving the configuration (`PUT`/`PATCH /api/config`) and a
  config-file reload now reject a config that references a model that does not
  exist in `models` (the reload keeps the previous config and raises the
  config-file-invalid alert); startup instead drops such references with a
  `removed reference to unknown model` warning and continues without rewriting
  `config.json`. A reference to a model that exists but is disabled is allowed
  and logged as a warning. Note: a config that omits `agents.defaults.models`
  inherits the default `Claude CLI` / `Codex CLI` aliases, so if those models
  were removed, set a default model before the next save.

### Removed

- **`launcher-config.json` is no longer read.** The retired launcher's
  separate IP allowlist file was folded into `gateway.allowed_cidrs` on every
  load. Nothing writes that file any more; if one is still present, startup
  logs a warning naming it and the allowlist in `config.json` is what applies.
- **Upstream assets removed.** The `assets/` directory (upstream demo GIFs,
  logos and community images, 11 MB, referenced by nothing) and the retired
  `claw-web` screenshot are gone from the repository.
- **The standalone sub-agent tool loop is gone.** Sub-agents only ever ran
  through the agent's full pipeline; the lightweight fallback loop inherited
  from the upstream project was unreachable in a running gateway. Spawning
  without the full-pipeline runner now fails with the same error the
  synchronous path already returned. No behaviour change for a running
  gateway, which always has the runner.

### Fixed

- **Channels reconnect on their own instead of stopping.** A dropped Slack
  Socket Mode connection, or a Matrix sync that ended, used to stop that
  channel receiving until the gateway was restarted. Both now restart with
  backoff (2s doubling to 60s) for as long as the channel runs. Telegram, Slack,
  Discord, Matrix and SecMsg report their connection state, and an operator is
  alerted only when retrying cannot help: a rejected token (at once), or no
  working connection for ten minutes ("Channel connection down").
- **Telegram waits as long as Telegram asks.** On a `429 Too Many Requests`
  the bot now waits the `retry_after` Telegram gives plus one second, instead
  of retrying every two seconds, which could prolong the rate limit. Other poll
  failures back off from 2s to 60s. A 429 or a 5xx no longer raises a
  "Telegram polling failed" alert; that alert now means only that Telegram
  rejected the bot token (401).
- **The WebUI picks up a new deploy on the next reload.** The embedded
  frontend was served with no cache headers, so a browser could keep an old
  `index.html`, and the old page chunks it names, after an upgrade. The SPA
  entry and other unhashed files are now sent with `Cache-Control: no-cache`
  and the content-hashed `/assets/` files as immutable.
- **`claw.pid` is written before the gateway starts serving.** It was written
  after all services were up, so for a brief window a gateway that was already
  accepting connections was invisible to `claw status` and `claw sessions`.
  The integration suite tripped over that window on macOS; it now also polls
  for the file instead of checking once.
- **Renamed or removed tools on an external MCP server are picked up without a
  gateway restart.** The tool list of a server under `tools.mcp.servers` was
  read once at connect time, so after the server was restarted with different
  tools the agents kept calling the old names, and the MCP host kept publishing
  them, until the gateway was restarted. The list is now refreshed when the
  server sends `tools/list_changed`, when a liveness probe
  (`liveness_probe_seconds`) sees a different list, on any reconnect, and on
  the new **Reconnect** action on the MCP servers page
  (`POST /api/mcp/servers/{name}/reconnect`), which also forces a server out of
  its post-failure cooldown. On each refresh the server's previous tools are
  removed from every agent before the current list is registered, and the MCP
  host catalogue follows, so stale names no longer linger in either place. See
  `docs/mcp.md`, "Tool list refresh".

- A turn interrupted by a restart is now replayed on the channel and chat it
  came from, so the user receives the answer; previously the replay ran on an
  internal channel whose reply was silently discarded (and tool side effects
  ran with no visible result). The user first sees "I was restarted while
  working on your last request — here is the result; resend it if anything is
  missing." Recovery replays a given message at most twice; after that it
  stops and sends "I was restarted while working on your last request and
  could not finish it. Please resend it if it still matters.", so a message
  that crashes the process can no longer crash-loop it. If the originating
  channel has been removed from the config the interrupted turn is dropped and
  logged. The source is kept in each agent's `state/state.json`
  (`pending_turns`).
- A channel whose start kept failing (bad token, service unreachable, port in
  use) was abandoned after 10 retries until a gateway restart or config
  reload. The channel manager now keeps retrying indefinitely, backing off
  from 5 seconds to a 5-minute ceiling (`StartRetryMin`/`StartRetryMax` in
  `channels/tuning.go`); the "Channel failed to start" alert is still raised
  once, on the 10th failed retry, and the channel comes up on its own when the
  cause clears.
- The device gateway listener was never restarted if it failed: devices could
  not connect until the gateway was restarted. It now re-listens with backoff
  (2 seconds to 1 minute), including when the port is temporarily in use,
  raises "Channel receive loop stopped" once per outage, and logs "Device
  gateway listener restored" when it is back.
- LINE: a webhook message whose mention `index`/`length` values were out of
  range or overflowed could crash the gateway; such mentions are now ignored.
- Two messages arriving for a brand-new session at the same moment could leave
  the session with a revoked MCP session token (session-scoped tools then
  failed until the session was cleared). Creation is now serialised per
  session so exactly one token is issued.
- `busy_timeout`, `synchronous` and `foreign_keys` were set on only the first
  SQLite connection of the device pairing store and the Fusion token store;
  connections the pool opened later under load ran without them and could fail
  a contended write immediately with `database is locked` instead of waiting.
  The settings now travel in the connection string so every connection gets
  them.
- Sub-agent task status/result files and imported SKILL.md files are written
  atomically (temp file + fsync + rename), so a crash mid-write can no longer
  leave a truncated file.

- A context-window overflow reported by OpenAI-compatible endpoints as HTTP 400
  `context_length_exceeded` (or by Anthropic as "prompt is too long") was
  treated as a bad-request error and the turn failed with "All models failed";
  it is now recognised as a context-limit error, so the history is compressed
  and the request retried as it already was for HTTP 413.
- The Backup section of the Config page no longer describes per-day folders or
  configuration-only snapshots.

- Deleting a model in the WebUI left agents still referencing it. Such an agent
  then sent the alias itself as the model id on every turn and got a 400 (for
  example OpenRouter's "is not a valid model ID") before falling back to the
  next model; the visible sign was `/model` listing an entry with no provider.
  Unresolvable aliases are now dropped from the fallback chain with a
  `fallback alias dropped` log line, renaming a model also repoints
  `subagents.models`, and clearing the default model removes the slot instead
  of leaving an empty entry.

## [0.5.6]

### Changed

- **Dependencies updated.** Maestro v0.5.3, whose QA prompt now defines the
  `fail` and `escalate` verdicts the same way as its documentation and runner
  (`fail` sends work back to the worker, `escalate` does not), plus the
  Anthropic SDK, mautrix and other third-party libraries. No other behaviour
  change intended.
- **`make test` is the full gate and `make` builds only after it passes.**
  `make test` runs the format check and `go vet`, then `test.sh`: the Go
  suite with the race detector and coverage, the frontend typecheck and unit
  tests, and the MCP integration suite, ending with a pass/fail summary and a
  non-zero exit on any failure. golangci-lint is found on `PATH` or in the Go
  bin directory, and a missing linter is reported with its install command.
  Lint is temporarily excluded from the gate until the remaining non-critical
  findings are addressed; `make lint` still runs it. `make check` now aliases
  `make test`. `make test-maestro-host` and `make check-webui` stay separate
  because they bind ports or need a running instance.

## [0.5.5]

### Security

- **Maestro `file_import` now obeys the agent's read sandbox.** It used to
  import any absolute path the claw process could read, regardless of
  `tools.allow_read_paths`. It now accepts only what the agent's own file tools
  can read: the workspace, every configured mount (read-only or read/write) and
  `allow_read_paths`. Anything else is refused with an error naming the path.
  Widen an agent's reach by adding a mount in the WebUI, not by configuring
  Maestro.

### Changed

- **BREAKING: the per-agent `maestro` setting is now an object.** Replace
  `"maestro": true` with `"maestro": {"enabled": true}`. The old boolean is not
  honoured: the gateway still starts, logs a warning naming the agent, and runs
  that agent without Maestro until it is re-enabled (the WebUI agent page has
  the switch). The block also carries runner settings, editable in the WebUI:
  `max_concurrent` (default 5), `rate_limit_requests` / `rate_limit_period`
  (default 10 per 60 s) and `allow_parallel` (default true). Runs stay
  sequential unless the LLM asks for a parallel run; `allow_parallel: false`
  refuses such requests and runs sequentially, with a note in the project log.
- **Maestro tasks can name a host model.** `llm_model_id` and `qa_llm_model_id`
  on Maestro task tools are passed to ClawEh as one of the agent's model
  aliases (the same names the spawn tool accepts). Leave them empty for the
  agent's default model. An alias the agent is not configured for fails the
  task immediately instead of retrying. The `maestro_start_here` guide now
  describes this instead of the standalone `llm_*` tools.
- **Agent mounts appear in Maestro's reference domain.** Every mount is
  available to `maestro_file_*` with `source=reference` and to playbooks via
  `instructions_file_source: "reference"`, under the mount's name, read-only.
  The automatic `maestro` mount is not mapped.
- **Maestro's operational log goes to the central logger** (component
  `maestro`, tagged with the agent), so it appears in the WebUI log viewer and
  rotates with `claw.log`. `<workspace>/maestro/maestro.log` is no longer
  written. Maestro's per-project logs are unchanged.
- **Maestro-enabled agents get a short system-prompt rule** saying what Maestro
  is for and to call `maestro_start_here` first.
- **Progressive discovery keeps `maestro_start_here` visible** for
  Maestro-enabled agents, so the model can always reach the guide and search
  for the rest. `always_shown_namespaces` entries are prefixes, so a full tool
  name pins that one tool. The MCP host is unaffected and still serves the full
  tool list to CLI providers.
- **Maestro workers are bounded by `turn_timeout`.** Each Maestro worker, QA
  and revision prompt runs as a sub-agent with the agent's turn timeout; a run
  that exceeds it fails and is retried within the task set's limits.
- **Maestro task results record usage.** Result files carry input/output/cache
  tokens, cost, the model that answered and the number of LLM iterations for
  each worker, QA and revision call.
- **Maestro tools are withheld when no sub-agent runner is available** for the
  agent (logged as a warning) instead of registering and failing every task.
- **New `make test-maestro-host`** runs Maestro's full MCP regression suite
  against a live gateway with a stub model. It needs `probe`, `jq` and `zip`
  and binds local ports, so it is a separate target.

### Fixed

- **Maestro workers now respect `max_subagent_depth`.** A Maestro worker that
  dispatched further Maestro tasks restarted the depth count at zero, so the
  recursion bound did not apply. Depth is now carried through `task_run` and
  `task_dispatch`; a worker at the bound fails the task without retry.

## [0.5.3]

### Changed

- **Third-party dependencies updated** (Anthropic SDK 1.72, `golang.org/x`
  libraries, fasthttp, gomarkdown and others). No behaviour change intended.

### Fixed

- **Cognitive-memory tools reject wrong-typed arguments instead of guessing.**
  A string `"true"` for `set_sticky` used to clear stickiness; a non-integer
  `limit` was silently rounded. Each `cogmem_*` tool now returns an error that
  names the argument and the type it expects. `cogmem_domain_migrate` names the
  destination when it is unknown, `cogmem_domain_list` rejects a status other
  than `active` or `archived`, `cogmem_memory_forget` rejects an unknown domain
  and retires every match rather than the first hundred, `cogmem_domain_update`
  clears the summary on an empty `set_summary` and rejects an empty `set_name`,
  and `cogmem_status` reports the domain and memory counts its description
  promised.
- **Memory import is idempotent and reaches archived domains.** Importing a
  portable memory document a second time no longer re-creates domains that were
  archived in the meantime; a matched domain takes the document's fields, and a
  memory the document marks retired is retired in the store. The import result
  gains `domains_updated` and `memories_retired` counts.
- **Memory store connections all carry their settings.** Only the first pooled
  SQLite connection used to receive the busy timeout and foreign-key setting,
  so a contended write on another connection could fail at once with
  `SQLITE_BUSY`. Every connection now gets them.
- **A failed consolidation records no applied changes.** The run's transaction
  rolled back, but the run record still claimed the operations it had tried.
  Archiving a domain is logged as an `archive` audit event rather than
  `update`, and an archived domain's `archived_at` follows a status change made
  through `cogmem_domain_update`.

## [0.5.2]

Cognitive memory stands on its own. It keeps its own copy of the conversation
until it has learned from it, so nothing else has to hold messages on its
behalf, and an assistant is only told about memory when it actually has it.

### Added

- **Listen jobs: a persistent callback from a long-poll tool.** `cron_schedule
  add` with `listen: true` and a `watch_tool` keeps the tool running in the
  background: call it, wait for it to return (an event, a dropped connection,
  or `watch_timeout_seconds`, default 300), and call it again at once. Whenever
  the `watch_fields` are present, `message` is delivered to the agent followed
  by the tool's full result, in an envelope that names the source (`The
  following event was received by a continuous monitor at <time>:` then
  `<tool> returned the following:`), distinct from the cron-fire envelope so
  events are never deduplicated as repeated fires. Every result with the
  fields present is delivered, identical or not; `suppress_repeats: true`
  withholds a result identical to the last delivered event, for a source that
  replays it on every reconnect. An absent field is "no data", not a change;
  failures retry with backoff and are reported after five in a row. Jobs of
  schedule kind `listen` have no next run and show as `listen (continuous)`.
  See docs/cron.md, "Listen jobs".

### Changed

- **Cognitive memory no longer reads the session archive.** Each message is
  handed to memory as it is spoken and held in the memory store's own inbox
  until the next background consolidation run covers it, then dropped. The
  archive is no longer marked or guarded on memory's behalf. On the first turn
  after upgrading, messages already archived but not yet consolidated are
  copied into the inbox once, so nothing spoken before the upgrade is lost to
  memory. Nothing to configure.
- **Assistants without cognitive memory are no longer told how to use it.**
  The memory rule in the system prompt is emitted only for agents that have
  `cogmem` on. Agents with it on receive byte-identical prompt text.
- **BREAKING: each assistant now has one memory, at `cogmem/cogmem.db` in its
  workspace, shared by every session it holds.** Memory used to be one file
  per session under `sessions/`, so under the isolating session modes
  (`session.mode` of `per-user`, `per-platform`, `per-account`) an assistant
  kept a separate memory per person or platform; it now keeps one, which is
  what "isolation is a property of the agent" always meant. The `cogmem/`
  directory is self-contained: it survives deleting the sessions, can be
  backed up on its own, and can be copied to a new assistant. Migration is a
  one-time step, with the service stopped, for each agent workspace:

  ```
  mkdir -p <workspace>/cogmem
  mv <workspace>/sessions/agent_<id>_main.cogmem.db <workspace>/cogmem/cogmem.db
  ```

  (move the `-wal` and `-shm` siblings too if present). A workspace with no
  such file needs nothing; memory starts empty on first use. Other per-session
  memory files, if any, hold separate memories that cannot be merged
  automatically; import them through the memory page if you want them. The
  memory page's store ids are now agent names rather than session file names.
- **BREAKING: each session's live window and state now live inside its
  `<key>.archive.db`.** The `<key>.jsonl` window and `<key>.meta.json` state
  files are no longer read or written; the archive DB a session already had
  now holds them too, so there is one file per session under `sessions/`.
  Migration is a one-time step, with the service stopped:

  ```
  claw sessions migrate
  ```

  It folds every `.jsonl` + `.meta.json` pair in each assistant's sessions
  directory into that session's archive DB, prints one line per session, and
  renames the sources to `*.migrated`; delete those once you have verified
  the conversations. It refuses to run while the gateway is up and is safe
  to re-run (already-migrated sessions are skipped). The gateway refuses to
  start while unmigrated `.meta.json` files remain in any assistant's
  sessions directory, naming the directory and this command, so a start
  before the migration cannot strand the old history. Deleting a
  conversation from the WebUI now removes the whole session store, archive
  included, rather than only the live window.
- **The context engine now lives in its own module, `github.com/PivotLLM/ctxengine`.**
  Transcript, archive, assembly, eviction, compaction and the `session_*`
  tools moved out of ClawEh unchanged; the assembled prompt is byte-identical
  (held by a golden test) and nothing changes on disk, in tool names or in
  the HTTP API. Compaction and cognitive-memory consolidation now share one
  model caller, so the summarization chain, its fallbacks and cooldowns
  behave the same for both.
- **Cognitive memory now lives in its own module, `github.com/PivotLLM/cogmem`.**
  ClawEh embeds it; nothing changes on disk, in the tools or in the API. The
  `cogmem_consolidate` tool's reply when no background worker is running now
  says so plainly instead of "queued (worker not yet running)".
- **Safety-net compaction now measures the whole request on every dispatch.**
  Between tool calls, the emergency compaction check considered stored history
  alone; it now also counts the system prompt, memory blocks and tool schemas,
  as the turn-start check always did. A turn that grows past the safety line
  mid-way compacts before the request is sent instead of relying on the
  provider's context-exceeded retry.

### Removed

- **Legacy `.json` session files are no longer read or migrated.** Sessions
  from before the JSONL store (a single `<key>.json` per session) were being
  converted on startup and read by the WebUI history as a fallback; both
  paths are gone. Any such file still on disk is ignored.
- **BREAKING: the config key `memory.retention.protect_unconsolidated` is
  gone.** It guarded the session archive from retention pruning until memory
  had consolidated a message. Memory now keeps its own copy of what it has not
  yet consolidated, so there is nothing left to guard. Migration: delete the
  key from `config.json`; leaving it in place is harmless, an unknown key is
  ignored on load.

### Fixed

- **Per-session settings survive compaction.** `/model`, `/reasoning` and
  `/tools` choices are stored in the same record as the compaction counters,
  and every compaction, clean shutdown and `/clear` used to overwrite that
  record wholesale, so a restart after a compaction forgot them. The context
  engine now rewrites only the fields it owns.
- **Repeated scheduled fires no longer count towards compaction after a
  restart.** The store counted a repeated cron fire as noise, but the engine
  kept its own count of every message and wrote it over the store's on
  compaction and shutdown. The store's count is now the only one.
- **Context-window recovery keeps message numbers and no longer strands
  results.** When a provider rejected a request as too large, the recovery path
  renumbered the retained messages past the archive, so `session_messages`
  could not fetch what the live window showed and the next summary's
  references were dropped as out of range. It also removed a tool-call turn
  while leaving its results behind. Recovery now drops whole turn groups,
  keeps their numbers, and truncates an oversized tool result in the current
  turn instead of giving up, so the retry can succeed.
- **A failed summarisation no longer duplicates the previous summary in the
  archive's checkpoint log.** When every summarisation model fails, the engine
  keeps the existing summary and trims the window; it used to record that
  summary as a new checkpoint each time.
- **Cross-agent session scoping and isolation fixes.** When mentioning an agent
  on a channel bound to another agent, mention extraction now runs before route
  resolution, preventing session key inheritance or cross-agent tool access.
  Session key resolution rejects foreign-scoped agent keys. Supervised background
  task restarts now preserve the originating owner agent ID and session key so
  task results are not routed to the default agent. `msg_send_file` verifies
  symlink resolution against the workspace to prevent sandbox escape.
- **Session files for keys containing `/` or `\` now resolve in the WebUI.** A
  Telegram forum thread or a Slack thread produces such a key. Every session
  file is named by one shared rule; the WebUI session view carried its own copy
  that only replaced `:`, so it looked for a file that did not exist.

## [0.5.0]

Cognitive memory redesign. The classification the model had to reason about at
every write shrinks to one field, the confirmation gate that was never used is
gone, and memory gains a type for things that happened — kept out of the prompt
and reachable by search.

### Removed

- **BREAKING: the `cogmem_memory_confirm` tool is gone.** Memories were written
  as `review` and waited for the assistant to ask you to confirm them. In
  practice it never asked: across seven agents, 244 memories were pending, the
  oldest 77 days, with zero confirmations. The gate is removed rather than left
  as dead weight. Nothing to migrate — confirmation was the only thing the tool
  did, and the memories it was gating are now active.
- **BREAKING: the config keys `memory.prompt.pending_surface` and
  `memory.prompt.pending_max` are gone.** They controlled the pending digest,
  which no longer exists. Remove them from your config; an unknown key is
  ignored, so nothing breaks if you leave them.
- **BREAKING: memories no longer have a `source` field.** It recorded whether
  the model believed a memory came from you or was inferred, gated nothing, and
  never reached the prompt. `origin` (`chat`, `consolidation`, `user`) records
  where a memory actually came from and is shown to both you and the assistant.
  Dropped from the database, `GET /api/memory/{id}` and the memory page.
- **BREAKING: memories no longer have a `priority` field.** It was stored and
  returned by the API and never read by anything.

### Added

- **A Local CLI agents section on the Providers page, with one switch each.**
  Claude, Codex, Antigravity and Cursor are listed whether or not their binary
  is installed — a CLI ClawEh supports but the host lacks is a different thing
  from one it does not support, so a missing one is greyed out and names the
  binary it looked for. The switch is the whole setup:

  - **On** creates the provider and a model if they are missing, with the
    protocol's own request timeout and a model id that lets the CLI choose its
    own model. An existing model is enabled in place, keeping whatever has been
    set on it — a switch is not a reset.
  - **Off** disables every model that runs through that CLI, not just one, so a
    switch carrying the CLI's name means the CLI. Nothing is deleted either way,
    so switching back on finds what it left behind.

  Setting one up by hand previously meant creating a provider on one page and a
  model on another, and knowing four values that appear in no form. One of them
  could not be set through the Web UI at all (see the `extra_args` entry under
  Fixed), so a CLI model created in the browser could not be made to work.

  Each row shows the **whole command line**, in the order it is built: the
  flags the provider always passes (`-p --output-format json`, and the stdin
  marker), the permission flags (`--dangerously-skip-permissions`, `--yolo`),
  and anything the CLI's models add in `extra_args`. Some of it auto-approves
  tool use, and none of it appeared anywhere before — the transport flags are
  not configurable and so were invisible. Someone asking what ClawEh runs on
  their machine is owed all of it, not the part that happens to live in config.

  CLI providers now appear only in this section, and the wire-protocol picker
  under **Add Provider** no longer offers `*-cli` protocols. Editing one — to
  pin an explicit binary path, say — is still available from its row.

- **Two new memory types.** `event` for something observed at a point in time —
  a trip, a delivery status, a scheduled run — and `operational` for the
  assistant's own housekeeping, such as where it files things and how it works.
  The full set is now `fact`, `preference`, `rule`, `event`, `operational`.
- **`event` memories stay out of the prompt.** They go stale and accumulate
  without bound, so they are never loaded automatically. Each domain reports how
  many it holds and names the call that reads them, and `cogmem_memory_search`
  reaches them with `include_events: true`.
- **Consolidation can tell which of two conflicting memories is newer.** Each
  memory it reviews now carries `age_days` — how long ago it was asserted — and
  they are listed oldest first. Previously it saw no time at all, so the rule
  that a newer instruction overrides an older one could not be applied to
  anything already stored: asked to resolve a contradiction, an assistant would
  correctly decline to guess which of two opposing instructions was current, and
  both stayed in its prompt indefinitely. Days rather than a timestamp, because
  the question is only which is newer.
- **Consolidation now tidies the domains it touches.** Where two memories say
  the same thing it retires the weaker one, and where a newer memory
  contradicts an older one it retires the older and records it in the conflict
  ledger — even when the conversation did not raise the topic. Nothing
  previously revisited a memory once written: de-duplication only ever ran
  against the current batch, so redundancy and stale contradictions
  accumulated indefinitely. One production agent had two active rules giving
  opposite instructions about the same notifications.

  It retires rather than rewrites, on purpose: several specific facts that
  merely share a topic are worth more than one vague paragraph, so only
  memories that genuinely say the same thing are collapsed. Automatic
  de-duplication by exact text match still runs as well.
- **A Status page in the Web UI**, reached from a **Status** entry at the bottom
  of the sidebar — outside the collapsible groups, since it describes the
  running process rather than a section of the configuration, and it is what you
  open when something feels wrong. It shows uptime, memory, and the number of
  assistants, channels, enabled models, configured providers and goroutines,
  over a detail box carrying the pid, version, build, Go toolchain, host OS and
  architecture, and whether the MCP host is running. Backed by a new
  `GET /api/system/status`, polled every five seconds so the figures stay live.

  Models are counted as *enabled* and providers as *configured*, not as the
  length of their config lists: an install carrying 40 model definitions of
  which 3 can run is described by the 3.
- **`/status` in chat now reports memory.** The command runs inside the process,
  so it reports on itself — which also means you can ask an assistant how much
  RAM it is using without shell access to the host.
- **`claw status` reports whether ClawEh is running, and its RAM.** It could
  previously answer neither: it reads configuration from disk and never looked
  at the process, so it described an installation rather than a running system.

  ```
  ClawEh:          running (pid 1690872), 40.4 MB RAM
  ```

  ClawEh writes `claw.pid` into its data directory at startup and removes
  it on clean shutdown. The data directory is the scope that matters — one
  binary runs several instances on a host, and the command has already resolved
  `CLAW_HOME` to find the config, so it reports on the instance you asked
  about. A stale file left by a hard kill reads as "not running": the process
  must still exist *and* still be claw, because pids are recycled.

  The figure is resident set size, the same number `ps` reports as RSS — not
  virtual size, which for a Go process includes a gigabyte of reserved address
  space and would suggest ClawEh is enormous when it is not.
- **Memory retention.** `event` memories are deleted after **30 days** and
  retired memories **90 days** after they were retired, both overridable per
  agent on the Agents page (blank = the default, `-1` = keep forever). Events
  stop being useful long before they stop accumulating — one agent recorded an
  hourly "nothing changed" note and reached 300 rows — and retiring leaves the
  row behind, so a store that retires steadily grows forever while showing
  nothing for it. **Only those two are ever deleted by age:** a `fact`,
  `preference`, `rule` or `operational` memory is permanent, so no retention
  policy can silently drop a standing instruction. The sweep runs as part of
  consolidation and logs what it removed.

  Retention is deliberately not something the model sets per memory. It already
  makes that judgement by choosing the type — "I drove to the KOA on 4 Sep" is
  an `event`, "we go to the KOA every Labour Day" is a `fact` — and a second
  knob would reopen the multi-field guesswork the type redesign closed.
- **The WebUI memory page is now a curation surface.** Change a memory's type,
  retire and restore it, show retired memories, and add a memory or a domain by
  hand. A memory you add yourself is recorded with `origin: user`, which the
  assistant sees.
- **Bulk curation.** Select many memories — or a whole domain at once from its
  header — and retype, retire, restore or delete them together. A domain that
  accumulated several hundred near-identical entries is the case the page exists
  for, and one row at a time is not a job anyone starts.
- **YAML export and import.** `GET /api/memory/{id}/export` downloads a full
  dump — domains, memories, every field, with a format version — and import
  loads one back in **merge** mode (add what is missing) or **replace** mode
  (wipe and load). IDs are re-minted on import, so a dump can be loaded into a
  different agent to seed it.
- **An automatic snapshot before every schema migration.** The database is
  copied to `<name>.pre-v<N>.db` beside itself before a migration runs, so an
  upgrade is recoverable without preparation. The snapshot is named for the
  version the store came *from*, which can differ between agents.
- **Cognitive-memory databases are migrated when the agent loads**, at startup
  and on config reload, rather than whenever each session next happens to be
  opened. Lazy migration spread a schema change across hours of ordinary use
  with no point an operator could call it done, and left a store belonging to an
  agent nobody talked to that day on the old schema indefinitely. Each upgrade
  is logged with its versions and the snapshot path, and a database that cannot
  be migrated is reported at startup instead of surfacing mid-conversation.

### Changed

- **BREAKING: the Gemini CLI provider is replaced by Antigravity (`agy`).**
  Google has deprecated the Gemini CLI. The `antigravity-cli` protocol takes its
  place, seeded as the **Antigravity CLI** provider and model, and the setup
  wizard detects `agy` alongside the other CLI agents.

  **`gemini-cli` remains accepted as an alias** and now runs `agy`, so an
  existing configuration keeps starting, keeps auto-starting the MCP host, and
  keeps working. New configurations should use `antigravity-cli`. The seeded
  Gemini model and its `GEMINI_CLI_TRUST_WORKSPACE` environment variable are
  gone; if you had customised that model, point it at the new provider.

  `set-mcp.sh` now registers claw with `agy mcp add` instead of `gemini mcp add`,
  and reports what to add for Cursor, which has no `mcp add` subcommand and is
  configured through `~/.cursor/mcp.json`.
- **A `retire` operation may omit its `evidence`.** Every memory operation had
  to cite a message in the current batch, which is right for anything that
  writes text — the rule exists to keep asserted memories anchored to something
  the user actually said. A retire asserts nothing and names a memory that must
  already exist, and housekeeping is by definition not raised by the current
  conversation, so the requirement made every tidy-up operation invalid. A
  single invalid operation rejects the whole payload, so an agent following the
  new rule above would have aborted entire consolidation runs.

- **BREAKING: the consolidation prompt is no longer overridable.** A workspace
  `COGMEM.md` used to replace it wholesale. Its contents are now **appended** to
  the built-in prompt instead, so the file holds instructions for that assistant
  — what to record, what to leave alone — while the rules and the output schema
  stay with the engine.

  The old arrangement made the machine contract operator-editable and froze it
  at whatever version each workspace was seeded with, so a change to the schema
  reached no existing agent. That survived by luck rather than design: a
  required field the old copy did not emit would have failed validation on every
  operation, and one invalid operation rejects the whole payload — silent, total
  consolidation failure across every agent, visible only in run records.

  **A `COGMEM.md` that is a copy of the built-in prompt is ignored**, with a
  warning naming the file, because appending one would show the model two
  contradictory output schemas. Every workspace seeded by an earlier version
  contains exactly that, so those agents fall back to the built-in prompt with
  no action needed: reduce the file to your own instructions, or delete it. New
  workspaces are seeded with a short commented stub instead of a copy of the
  prompt.
- **BREAKING: `cogmem_export` writes YAML, not Markdown.** The output moves from
  `files/MEMORY_EXPORT.md` to `files/MEMORY_EXPORT.yaml` and is the same format
  the WebUI exports — which means it can be read back. The Markdown projection
  could only be looked at.
- **Memory lines in the prompt now show their type**, so the assistant can tell
  a standing rule from a stale observation: `- (rule) Do not use the word
  "thuddy."` Previously only the text was shown.
- **The prompt tag `[source: …]` is now `[origin: …]`.** It always rendered
  `origin`; with no `source` field left, the old label was actively misleading.
- **BREAKING: existing agents keep their old consolidation prompt, and must be
  updated by hand.** Each agent workspace holds a `COGMEM.md` seeded from the
  shipped template and never overwritten afterwards — which is the point of it,
  but means an upgraded install keeps the prompt it was seeded with. That prompt
  still produces valid output, so nothing fails: the agent simply never records
  an `event` or `operational` memory, and the most useful part of this release
  never reaches it. Delete the seeded copies to pick up the current prompt:

  ```
  rm ~/.claw/agents/*/COGMEM.md      # or $CLAW_HOME/agents/*/COGMEM.md
  ```

  They are re-seeded from the new template on the next start. **If you have
  edited one, keep it and add the new types yourself** — the file is yours.
  ClawEh now logs a warning naming any agent whose prompt predates the new
  types, so a missed one is visible rather than silent.
- **Consolidation states a memory's type and nothing else.** It no longer sets
  `status` (there is no longer a choice) or `source` (gone), and an operation
  that omits a required field is rejected rather than silently defaulted.

### Fixed

- **BREAKING: CLI models no longer need `extra_args` set, and the flags are now
  supplied automatically.** Every CLI agent needs a flag to run unattended
  (`--dangerously-skip-permissions`, `--yolo`, and so on). It was stored on each
  model, could not be set anywhere in the Web UI, and left out it failed
  silently: the CLI auto-denies the tool call it cannot prompt for and reports a
  completed turn with an empty answer, so the assistant answers chat and goes
  mute the moment it tries to do anything. ClawEh now supplies each protocol's
  required flags itself and appends whatever `extra_args` adds, so a model
  written before this — or added through the Web UI — works without being
  edited. A model that already lists the flag does not get it twice.

  The breaking part is that the flags can no longer be removed by clearing
  `extra_args`. If you deliberately run a CLI sandboxed, say so and we will add
  the opt-out.

- **A CLI that refused a tool call reported success and said nothing.** The
  Antigravity CLI returns `status: SUCCESS` with an empty response and a
  `denied_actions` list when it is not allowed to act; ClawEh did not know the
  field existed and handed the empty answer on with no error and nothing in the
  log. It now fails with the refused action and the flag that fixes it.

- **BREAKING: deleting a provider silently changed the settings of others.**
  Configuration is loaded by overlaying the file onto the built-in defaults, and
  deleting a provider shifted every later entry onto a different default, which
  it then inherited unset fields from. Deleting `OpenAI` gave `OpenRouter Chat`
  Groq's `no_parallel_tool_calls` and `NVIDIA` OpenRouter Strict's
  `strict_compat` — wire-behaviour changes to providers nobody touched, made
  permanent by the next save. **Check your `providers` for `strict_compat` or
  `no_parallel_tool_calls` on an endpoint that should not have them** if you
  have ever deleted a provider; remove the key and restart.

- **A dormant agent compacted twice in a row on waking.** Compaction has an age
  trigger — it fires once the oldest message in the live window passes
  `trigger.days` (7), however little of the window that window holds. The pass
  cannot always cut back to the retention age cap (5 days): the last messages are
  kept whatever their age, and the latest user turn is kept even when it is over
  the cap, deliberately, since a request with no user message is rejected
  outright. A session dormant past the trigger therefore comes back holding a
  message the pass was never going to remove, and the next message fired another
  pass against the same boundary — on production, a second compaction 109
  seconds after the first, summarizing 36 tokens. The age trigger now waits for
  the window to move past a boundary it has already compacted against. Nothing
  to change: the intended gap between trigger and retention (2 days of quiet)
  now holds in the case where it did not.

- **The model count is shown on every configured CLI row.** It was omitted
  where a CLI had a single model, which left it printed for one CLI and absent
  for the rest — read as a fault rather than as brevity.

- **Plural translations never selected their plural form.** Keys carrying a
  `_plural` sibling — the pre-v21 i18next convention — are silently ignored by
  the version in use, which wants `_other`. Provider cards read "2 model", and
  the new CLI rows inherited it. Both fixed, with a test that fails on any
  `_plural` key left in the catalogue.

- **A CLI provider was offered five settings that do nothing.** Proxy,
  `strict_compat`, `require_reasoning_content`, `no_parallel_tool_calls` and
  `response_format_json` are HTTP wire knobs, and the CLI factory reads none of
  them — a CLI provider is built from its command, workspace, arguments and
  environment alone. Shown on a CLI form they were five controls with no effect,
  and an off switch reads as a feature that is available and disabled: it made
  `response_format_json` look like the reason a CLI was not returning JSON. It
  always does. `--output-format json` (`--json` for Codex) is in the arguments
  ClawEh passes, not in the configuration, and never was optional. The advanced
  section is now hidden for CLI providers.

- **The Providers page called a CLI provider configured whenever a path was
  filled in, without checking that the binary was there.** A provider pinned to
  a CLI that had since been upgraded or uninstalled showed a green dot and
  "Configured" while every request through it failed. The reverse was worse: a
  CLI provider with the **Command** field left blank — which is how the seeded
  config ships, and the more robust choice, since it follows the CLI across
  upgrades — showed as unconfigured even though it worked. The dot and the label
  now mean the binary actually resolves, decided in the ClawEh process against
  the `PATH` its subprocesses are launched with, and the card names the binary a
  blank command resolved to. CLI cards also carry a **Configured** /
  **Not configured** label at all: they previously rendered an empty space where
  every other card said what it was.
- **Adding a CLI provider no longer demands a command.** The field was
  mandatory, which forced a hard-coded path on every new CLI provider — the one
  configuration that breaks when the CLI is upgraded. Leave it blank and ClawEh
  runs the protocol's default binary from `PATH`. The field also no longer shows
  `/usr/local/bin/claude` as a placeholder, which suggested a path was expected
  and named the wrong CLI for three of the four protocols.
- **The wire-protocol picker offered `gemini-cli` and not `antigravity-cli`,**
  so there was no way to add an Antigravity provider through the Web UI. It now
  offers `antigravity-cli`; a provider already configured as `gemini-cli` keeps
  working — the backend treats it as an alias for the same binary — and still
  renders as a CLI provider.
- **`systemctl stop` and `systemctl restart` now shut ClawEh down gracefully.**
  Only `SIGINT` was handled, and systemd sends `SIGTERM`, whose default
  disposition kills the process outright — so every stop and restart skipped
  shutdown entirely: channels were never stopped cleanly, in-flight work was
  never drained, and the 15-second graceful-shutdown timeout was dead code on
  the one path production actually uses. Pressing Ctrl-C in a foreground run
  always worked, which is why it went unnoticed.
- **An operation that omitted both `status` and `source` was accepted and then
  defaulted to a combination the rules forbid** — `assistant_inferred` with
  `active`. Every guard tested for the fields being *wrong*, not missing. The
  fields are gone and the remaining ones are checked for presence.
- **Memories in `review` were unreachable.** They were excluded from the prompt,
  excluded from `cogmem_memory_search`, and excluded from the WebUI, and the
  digest that was meant to surface them showed a fixed top-eight by confidence —
  so 236 of the 244 had never been seen by anything. They are now active and
  visible.

## [0.4.72]

First release under the stable-compatibility policy: config schemas, tool names,
API shapes, and the device-gateway protocol are now things other installs depend
on, and breaking one is a deliberate decision rather than a free move.

### Security

- **`GET /api/config` no longer returns credentials.** It previously returned the
  whole configuration unmasked — provider API keys, bot tokens, device tokens,
  the WebUI channel token — on a surface with no operator authentication. Values
  are now masked (`sk-****cdef`). `PUT` and `PATCH` restore masked values from
  disk, so reading the config, editing it and writing it back does not destroy
  the credentials you never saw. Setting a genuinely new credential still writes
  through. Masking is driven by the JSON field name, so a credential added later
  is covered from the day it appears; list entries are matched by identity
  (`id`, `name`, `model_name`, `account`) so deleting or reordering bots,
  providers or models cannot move a credential onto the wrong entry.
- **The WebUI chat socket no longer disables its origin check, and no longer
  puts the token in the URL.** Setup wrote `channels.webui.allow_origins: ["*"]`
  into every install so a frontend dev server on port 5173 could connect — a
  development convenience that shipped to production and switched off the only
  defence against cross-site WebSocket hijacking, since CORS does not apply to
  WebSockets. It also set `allow_token_query: true`, putting the token in the
  URL where proxies, access logs, `Referer` headers and browser history record
  it. Setup no longer writes either.

  Empty `allow_origins` now means **same origin** (the `Origin` host must match
  the `Host` requested) rather than "allow any", which works however the
  operator reaches the UI — localhost, a LAN address, or a proxied hostname. An
  explicit list is still honoured verbatim, `"*"` included, which is what a
  frontend dev server needs.

  The browser now sends the token as a WebSocket subprotocol
  (`["claw-token", "<token>"]`) instead of a query parameter, because the
  browser WebSocket API cannot set an `Authorization` header. The server echoes
  only the `claw-token` marker, never the token. `Authorization: Bearer` still
  works for non-browser clients, and `allow_token_query` still works if set
  explicitly.

  **This affects `/webui/ws` only** — the browser console's chat socket. The
  device gateway on port 18791, which ClawToTalk and other OpenClaw-compatible
  apps use, authenticates inside the protocol rather than at the HTTP handshake
  and is untouched.

  **Existing installs carry `allow_origins: ["*"]` from a previous setup run.**
  Clear it (`"allow_origins": []` under `channels.webui`, or the Channels page)
  to pick up same-origin checking; nothing clears it for you.

- **The device gateway compares shared tokens in constant time with respect to
  length.** `subtle.ConstantTimeCompare` returns early when lengths differ, so
  the comparison leaked the secret's length. Both sides are now hashed to a fixed
  width first.

### Added

- **`file_count` — line, word, character and byte counts for a file, like
  `wc`.** Useful for sizing a file before reading it, and cheap enough to use as
  a change signal. Counts match `wc` exactly, verified in the tests against the
  real binary: `lines` is the newline count, so a file whose last line is
  unterminated reports one fewer than you would count by eye — `final_newline`
  in the result says which case you are in. `characters` counts Unicode
  characters and `bytes` counts bytes, which differ for non-ASCII text. The file
  is streamed, so it works on files too large for the read tools, and a file
  that is not valid UTF-8 is measured rather than rejected, with `invalid_utf8`
  marking the character count as untrustworthy.

- **Watch jobs: cron that only wakes the agent when something changed.**
  `cron_schedule` accepts `watch_tool`, `watch_args` and `watch_fields`. The
  named tool is called on the schedule **with no model in the loop**; the values
  at `watch_fields` are fingerprinted and compared with the previous run, and
  `message` is delivered only when they move. Polling "is there new mail?" no
  longer costs an LLM turn per check to conclude nothing happened.

  `watch_fields` are dot-paths into the tool's result. A path crossing a list
  applies to every element and collects the results, so `messages.id` is the set
  of ids currently present. Naming fields rather than hashing the whole result is
  what stops a probe firing on unread counts, timestamps and reordering. Omitting
  them compares the entire result.

  Behaviour worth knowing: the first run records a baseline **silently**, so
  creating a watch does not immediately report everything that already exists.
  A failed probe leaves the fingerprint untouched — advancing it would swallow
  the change that happened while the probe was broken — and after five
  consecutive failures the agent is told once, because a probe that has gone
  blind otherwise looks exactly like a quiet one. Probes run against the owning
  agent's tool registry, resolved on every run so a tool revoked in config stops
  being probed, and session-scoped tools are refused since a probe has no
  conversation to act on. Each probe is bounded at 60 seconds.

- **`claw install` now creates the `openclaw` symlink.** The Rabbit R1's
  `rabbit-agent` spawns `openclaw acp`, so the binary has to exist under that
  name. Only `make install` did this before, which meant an install from a
  release binary silently lacked the R1 path. Also documented: the README now
  has an **External devices** section covering both transports — the device
  gateway that OpenClaw-compatible apps such as ClawToTalk connect to, and the
  stdio ACP bridge the R1 uses — and how the bridge reaches the running gateway
  over loopback.
- **`--allowed-cidrs` accepts `private` and `any` as shorthands** for the RFC1918
  ranges and for any address, so the common headless choices do not require
  remembering three prefixes.
- **`gateway.allowed_cidrs` accepts `"*"`, meaning any address in either
  family.** `0.0.0.0/0` is the obvious thing to reach for and does not mean
  that — it is an IPv4 prefix, so on a dual-stack host it still refuses IPv6
  clients, which reads as the allowlist simply not working. CIDRs continue to
  mean exactly what they say (`0.0.0.0/0` is all IPv4, `::/0` is all IPv6);
  `"*"` is the unambiguous way to open it to everything, spelled the same as the
  wildcards in `allow_from` and `allow_origins`.

- **`claw network` — set who may reach the WebUI/API without editing
  config.json.** The recovery path for an install that listens on the network
  and then refuses every connection from it, which is what an empty
  `gateway.allowed_cidrs` looks like from a browser. With no argument it allows
  the private LAN ranges; it also takes a comma-separated CIDR list or one of
  `private`, `any`, `none`. `claw network --show` prints the current allowlist
  and changes nothing.

  ```
  claw network                    # 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16
  claw network 192.168.1.0/24     # one subnet
  claw network any                # any address — the WebUI has no password
  claw network none               # back to loopback only
  ```

  It writes the config and exits, so it is safe to run against a running
  gateway.

### Changed

- **`claw install` refuses a non-loopback `--host` without `--allowed-cidrs`.**
  That combination produces an install which listens on the network and then
  refuses every connection from it — indistinguishable from a firewall problem.
  It now fails at install time with the flags to fix it. An install whose config
  already carries an allowlist is unaffected.
- **BREAKING — an empty `gateway.allowed_cidrs` now means loopback only.** It
  previously fell back to the RFC1918 private ranges, so an install bound to
  `0.0.0.0` served the whole local network by default. Binding address and
  allowlist are now two independent gates: binding off-box makes ClawEh *listen*
  there, and the allowlist decides who is *served*.

  If you reach the WebUI from another machine and have never set
  `gateway.allowed_cidrs`, set it now or you will lose access on upgrade:

  ```json
  { "gateway": { "allowed_cidrs": ["192.168.1.0/24"] } }
  ```

  Use the three RFC1918 ranges (`10.0.0.0/8`, `172.16.0.0/12`,
  `192.168.0.0/16`) to restore the previous behaviour exactly, or `"*"` to allow
  any address. Loopback is always allowed. `claw install --allowed-cidrs` sets
  the same field, and the gateway logs a warning at startup when it is bound
  off-box with an empty allowlist.

- **Built with Go 1.27.1, and all Go dependencies updated to their latest
  releases.** Notable bumps: `github.com/mark3labs/mcp-go` 0.58.0 → 1.0.0,
  `modernc.org/sqlite` 1.57.0 → 1.58.0, `github.com/anthropics/anthropic-sdk-go`
  1.68.0 → 1.69.0, and `golang.org/x/crypto` 0.55.0 → 0.56.0. No config, tool,
  API or protocol behaviour changes with it; the `go` directive in `go.mod`
  stays at 1.26.7, so building from source still works with an older toolchain.
- **The version now carries a build number: `0.4.72+d4812df7 [20260902155301]`.**
  The commit identifies which source a binary came from, but a hash has no
  order, so it cannot answer "is the copy I am running newer than the one I just
  built?" — and a rebuild of the same commit is indistinguishable without it. The
  build number is the UTC link time (`yyyymmddhhmmss`), which always increases,
  compares correctly as plain text, and needs no version bump to change. It
  appears everywhere the version does: `claw version`, `claw status`, the startup
  log, the WebUI footer, `GET /api/system/version`, and the agent's system
  prompt.

  The release number and commit stay one unbroken token before the space, so a
  version truncated on paste still identifies its source. `SemVer()` is
  unchanged — bare `0.4.72` — so MCP, ACP and device-gateway handshakes, and the
  release tag, are untouched. A plain `go build` with no ldflags still reports a
  bare version.
- **`gateway.allowed_cidrs` now applies without a restart.** The allowlist was
  fixed for the lifetime of the listener, so widening it after locking yourself
  out meant restarting the service. A running gateway now picks the change up
  on its next config reload — about 15 seconds with the default interval and
  debounce — and because the listener is not recreated, open WebUI WebSocket
  connections survive it. An allowlist that
  fails to parse is refused and the running one is kept, rather than the reload
  dropping access to loopback.
- **The startup warning for a network bind with an empty allowlist now names the
  fix.** It previously suggested `0.0.0.0/0` for "any address", which is an IPv4
  prefix — it still refuses IPv6 clients, which on a dual-stack host reads as the
  allowlist being broken. It now points at `claw network` and, for the
  allow-everything case, at `"*"`, which covers both families.

### Removed

- **The `hw_i2c` and `hw_spi` tools.** Inherited from the picoclaw fork, where
  they drove sensors over the Linux I2C/SPI buses on the original SBC.
- **`docs/config.example.json` and `docs/env-example`.** The example config had
  drifted so far it no longer loaded, and described picoclaw's model shape rather
  than ClawEh's. ClawEh writes a complete `~/.claw/config.json` on first run,
  generated from the config types, so it cannot drift — that file is now the
  reference. Every variable in `env-example` was dead, including a Feishu channel
  this codebase has never had.

### Fixed

- Billing failures now surface OpenRouter's top-up URL, which it sends in
  `error.metadata.remedy_hint` rather than a `billing_url` field, and the
  provider is rechecked every 30 minutes.
- `docs/remote-access.md` described the WebUI and device gateway as sharing port
  18790. The device gateway is a separate listener on `channels.device.port`
  (default 18791); following the old text published the wrong port and left the
  gateway unreachable.
- `docs/tools_configuration.md` documented `use_bm25` / `use_regex` discovery
  keys and `tool_search_tool_*` tools that do not exist. The meta-tools are
  `search_tools` and `get_tool_details`, and discovery config lives at
  `tools.discovery`, not `tools.mcp.discovery`.
- `cmd/claw-auth/README.md` documented environment variables and a `-config`
  file that `claw-auth` has never read; OAuth client credentials come from the
  MCPFusion server. It also called the binary `fusion-oauth` throughout.
- A skills error lost its wrapped cause and read `skills directoryw %v`.
- **The gateway no longer crashes on the second config reload.** Any two config
  changes in the life of a process — two saves from the WebUI, two `claw network`
  runs, an edit to config.json followed by another — killed it with
  `panic: close of closed channel`. The mount watcher was stopped on every
  reload but only ever created at startup, so the second stop closed an
  already-closed channel. It is now rebuilt on reload like every other service,
  which also fixes the quieter half of the bug: after the first reload, external
  mount notifications had stopped firing for the rest of the process's life.
- **A dead MCP server is detected and reconnected again.** The liveness probe
  asked `Client.Ping`, which in `mark3labs/mcp-go` v1.0.0 returns success
  *without contacting the server* whenever the negotiated protocol is modern
  (2026-07-28 or later). Every MCP server therefore reported healthy forever,
  and a session that had gone away was never reconnected — the failure stayed
  invisible until a real tool call hit it. The probe now issues a `ListTools`
  round trip, which reaches the server on every protocol version.
- **`/ready` now reports ready.** It answered `503 not ready` for the entire
  life of every process. The readiness flag was only ever set by
  `health.Server.Start()`, which starts the health server's own listener — and
  the merged binary does not use it, registering the handlers on the shared mux
  instead. Readiness is now set once the listener is up and the channels have
  started, re-set after a config reload, and cleared on shutdown. `/health` is
  unchanged and still answers as soon as the port is open; the difference
  between the two is the point of having both.
- **The delete button on an agent card has an accessible name.** It was
  icon-only with no `aria-label`, so a screen reader announced nothing on the
  control that deletes an agent.
- **A successful memory consolidation no longer looks like a failure.** When the
  memory model proposed an inferred item as `active` instead of `review`, ClawEh
  corrected it, saved the memories correctly, and recorded a note saying so —
  but the note was stored in the run's `error` field, so the memory page showed
  it in red on a run that had actually succeeded. Runs now carry a separate
  `note`, shown as ordinary text; `error` means the run failed. Existing
  databases are migrated on open, and notes already stranded in `error` on
  successful runs are moved across, so the red text clears immediately rather
  than after the next run.
- **The WebUI devices page no longer fails intermittently with
  `store open failed`.** Roughly one gateway restart in three, `GET /api/devices`
  returned a 500 and the page showed an error. Opening the device pairing
  database re-set `journal_mode=WAL` on every call, and CONVERTING a database to
  WAL takes an exclusive lock that `busy_timeout` does not cover — SQLite
  returns `SQLITE_BUSY` immediately — so the admin API lost the race against the
  device channel opening the same file at startup. The mode is a property of the
  file and now it is only converted when it is not already WAL, with a short
  retry, and a database that stays on the rollback journal is logged rather than
  failing the open. The API also keeps one database handle for the process
  instead of opening and closing one per request.
- **The agent Tools page no longer shows a raw translation key as a heading.**
  Tools in the `common` and `memory` categories were grouped under
  `pages.agent.tools.categories.common` and `….memory`, because the catalog
  defines those categories and the UI had no labels for them. They now read
  "Common Directory" and "Memory".
- **Typing a channel's `allow_from` list no longer eats trailing separators.**
  The WebUI field resynced itself from the parsed value on every keystroke, so a
  `,` or space typed to start the next entry disappeared as soon as it was
  entered, and the entry had to be worked around rather than typed. Affects the
  Telegram, Slack and generic channel forms.

[0.5.3]: https://github.com/PivotLLM/ClawEh/compare/0.5.2...0.5.3
[0.5.2]: https://github.com/PivotLLM/ClawEh/compare/0.5.0...0.5.2
[0.5.0]: https://github.com/PivotLLM/ClawEh/compare/0.4.72...0.5.0
[0.4.72]: https://github.com/PivotLLM/ClawEh/compare/0.4.70...0.4.72
