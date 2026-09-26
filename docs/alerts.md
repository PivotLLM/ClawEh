# Operator alerts

ClawEh raises an alert when something happens that the operator should hear
about through a path other than the agent's own channels: a model that can
no longer authenticate, a channel that gave up, a scheduled job that failed.
Alerts are produced by the `github.com/tenebris-tech/alerter` module.

## Where alerts go

Every alert is written to a log: `<CLAW_HOME>/logs/alerts.log`, or the file
named by `ALERTER_LOG` (full path and file name) in the service environment.
The WebUI Logs page shows the alerts log when its source selector is set to
**Alerts**, and `GET /api/gateway/alerts?lines=N` returns the last N lines.

The module also delivers alerts to whatever channels are configured through
`ALERTER_*` environment variables (Pushover, SMS via Telnyx, SMTP mail, a
JSON webhook), read from the service environment or from `~/.alerter` in the
home directory of the user running ClawEh. See the module's README for the
variables; nothing in ClawEh changes when a channel is added or removed.

One record per alert: time, the priority (`NORMAL`, `URGENT` or `EMERGENCY`),
`ClawEh@<host>`, the event id in brackets when there is one, title,
description, then details indented:

```
2026-09-24T10:00:00-04:00 NORMAL    ClawEh@empire [claude-cli/claude] Model authentication or billing failure: claude-cli/claude parked for 1m0s after 1 consecutive failure(s): auth (status 401)
```

## Priority

The alerter has three levels: Normal, Urgent and Emergency. Every ClawEh
alert is **Normal**. Urgent and Emergency are for something that must reach
a person now, at any hour; how each level is delivered (for example the
Pushover priority) is the operator's choice in the `ALERTER_*` variables.
No ClawEh alert qualifies today, so the Priority column in `ALERTS.md` is
blank throughout; an alert promoted later gets `*` (Urgent) or `**`
(Emergency) there.

## What alerts

The full list, with comments, is in `ALERTS.md` at the repository root.

| Priority | Alert | When | Event id |
|---|---|---|---|
| | Model authentication or billing failure | A model is parked for an `auth` or `billing` failure (a CLI logged out, a key revoked, credit exhausted). Reported on the first failure; no retry fixes it | provider/model |
| | Model parked after repeated failures | A model reaches the settled category cooldown after the 1/3/5-minute escalation | provider/model |
| | MCP server unreachable | A reconnect or background connect attempt failed and the server is in cooldown | server name |
| | MCP host server stopped | ClawEh's own MCP server died after startup | `mcpserver` |
| | HTTP listener stopped | The loopback or HTTPS listener died after start; the gateway exits so the service manager restarts it | `http` |
| | Agent loop stopped | The agent loop returned an error | `agent-loop` |
| | Channel failed to start | A channel has failed to start ten times in a row; retries continue every five minutes | channel name |
| | Channel send failed | An outbound message was dropped after its send retries | channel name |
| | Channel receive loop stopped | The device gateway listener failed; it re-listens with backoff while the channel still reports running | channel name |
| | Channel connection down | A channel has had no working connection for ten minutes despite retrying (`ConnDownAlertAfter`, `channels/tuning.go`) | channel name |
| | Channel credentials rejected | Slack or Matrix rejected the channel's token | channel name |
| | Telegram polling failed | Telegram rejected the bot token (401) | channel name |
| | SecMsg account discovery failed | The SecMsg daemon could not be queried; no accounts bound until the next reload | `SecMsg (<name>)` |
| | SecMsg has no linked accounts | The daemon has no account to bind | `SecMsg (<name>)` |
| | Scheduled job failed | A cron job's handler returned an error, or the job could not be delivered | job id |
| | Cron store unreadable | `jobs.json` could not be read at startup | `cron-store` |
| | Cron store not saved | A job-state write failed | `cron-store` |
| | Session not saved | A conversation could not be written to its session store | `session-store` |
| | Service tokens not loaded | The service-token file could not be read | `service-tokens` |
| | Config file invalid | A config edit on disk could not be loaded or validated and was not applied | `config` |
| | Config reload failed | Applying a valid config failed part way; services may not all be running | `config` |
| | Nightly backup failed | The configuration backup did not run | `backup` |
| | Log rotation failed | The midnight log roll failed; file logging may be stopped | `logging` |
| | Fusion token store unavailable | The Google/Microsoft token database could not be opened; all Fusion tools disabled | `fusion` |
| | Maestro tools disabled | An agent's Maestro directory could not be prepared | `maestro:<agent>` |
| | Cognitive memory migration failed | An agent's memory database could not be opened or migrated | `cogmem:<agent>` |
| | Message-token store unreadable | An agent's token file is corrupt | `msgtoken:<agent>` |
| | Message-token store not written | A token change (including a revocation) could not be saved | `msgtoken:<agent>` |
| | Named message-token store unreadable | The named-token file could not be loaded | `msgtoken:named` |
| | Session state not persisted | A per-session setting could not be written | `session-store` |
| | Sub-agent record not written | A sub-agent status or results file could not be written | `subagent-store` |
| | Mount marker not written | A watched mount's seen-files marker could not be written | `mount:<path>` |
| | Voice transcription rejected | The transcription API answered 401, 402 or 403 | `voice:<provider>` |
| | Device source not started | A device event source failed to start | `devices:<kind>` |
| | USB device monitor stopped | The udevadm monitor stream ended | `devices:usb` |
| | Device store unavailable | The paired-device database could not be opened for a request | `device-store` |
| | Device authentication locked out | A client address failed device authentication five times in ten minutes | client IP |
| | WebUI login locked out | A client address failed five WebUI logins in ten minutes | `auth-lockout` |
| | Daily model spend over threshold | The day's model cost reached `agents.defaults.daily_spend_alert_usd` | `spend:<day>` |
| | Database failed integrity check | A SQLite store failed `PRAGMA quick_check` during a backup and was skipped | `backup:<path>` |
| | TLS certificate reload failed | A changed certificate or key file did not load; the old pair keeps serving | `tls-reload` |
| | TLS certificate expires soon | An operator-supplied certificate is within 14 or 3 days of expiry, or expired | `tls-expiry` |
| | Self-signed TLS certificate renewal failed | The self-signed certificate could not be regenerated before expiry | `tls-selfsigned` |
| | Session retention failed | The nightly session-retention pass could not delete an idle archive or a cogmem snapshot | `session-retention` |
| | Session store write failed | A message could not be written to the session store, so the turn was abandoned | `session-store` |
| | Context compaction breaker tripped | Three automatic compactions failed in a row; only the safety-net pass still runs | session key |

## Repeats

The same title with the same event id inside ten minutes is counted rather
than written again; the next record after the window says how many were
suppressed. A model that is logged out therefore produces one line, not one
per turn.

## Shared modules

Three failure classes live inside shared modules and are not alerted yet:
session archive open/append failures (ctxengine), consolidation failures
(cogmem) and OAuth token refresh failures (MCPFusion). Matching their log
lines through the logger bridges was rejected as fragile: the messages are
free text with no stable code or agent identity, and a level change upstream
would silently stop an alert.

The agreed design, to be done as its own project, is one typed error-event
hook per module, on the option type each already has, with ClawEh writing the
alert (title, priority, event id) in the hook body so no module depends on
the alerter or decides operator policy:

- ctxengine: `WithArchiveErrorHook(func(ArchiveError))` with `Op`, `SessionKey`,
  `Path`, `Seq`, `Err`; called from the archive open and append paths. ClawEh
  raises "Session archive not written" (id `<agent>`).
- cogmem: `WithRunErrorHook(func(RunError))` on the consolidation manager with
  `Job`, `Trigger`, `Stage` (factory or run), `Status`, `Err`. ClawEh raises
  "Memory consolidation failed" (id `<agent>`; retried on the next trigger,
  repeats collapse).
- MCPFusion: a typed `RefreshError{StatusCode, Body}` from the strategies so
  a revoked token (400/401) can be told from a transient failure, and
  `WithAuthEventHook(func(AuthEvent))` on the Fusion engine with `Kind`
  (refresh failed, client reported error), `Tenant`, `Service`, `AuthType`,
  `StatusCode`, `Message`, `Err`. ClawEh raises "OAuth token refresh rejected"
  (id `<agent>/<service>`) and "OAuth client reported error".

Each is a minor version bump of its module, then a `go get` in ClawEh and four
rows in `ALERTS.md`.
