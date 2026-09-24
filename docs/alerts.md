# Operator alerts

ClawEh raises an alert when something happens that the operator should hear
about through a path other than the agent's own channels: a model that can
no longer authenticate, a channel that gave up, a scheduled job that failed.
Alerts are produced by the `github.com/tenebris-tech/alerter` module.

## Where alerts go

Version 0.0.x of the module writes a log. ClawEh writes it to
`<CLAW_HOME>/logs/alerts.log`; set `ALERTER_LOG` (full path and file name) in
the service environment to write elsewhere. The WebUI Logs page shows the
alerts log when its source selector is set to **Alerts**, and
`GET /api/gateway/alerts?lines=N` returns the last N lines.

One record per alert: time, `HIGH` or `LOW`, `ClawEh@<host>`, the event id in
brackets when there is one, title, description, then details indented:

```
2026-09-24T10:00:00-04:00 HIGH ClawEh@empire [claude-cli/claude] Model authentication or billing failure: claude-cli/claude parked for 1m0s after 1 consecutive failure(s): auth (status 401)
```

Later module versions add delivery channels (mail, push services), each
configured by its own `ALERTER_*` environment variables; nothing in ClawEh
changes when they arrive.

## What alerts

The full list, with comments, is in `ALERTS.md` at the repository root.

| Priority | Alert | When | Event id |
|---|---|---|---|
| High | Model authentication or billing failure | A model is parked for an `auth` or `billing` failure (a CLI logged out, a key revoked, credit exhausted). Reported on the first failure; no retry fixes it | provider/model |
| Low | Model parked after repeated failures | A model reaches the settled category cooldown after the 1/3/5-minute escalation | provider/model |
| Low | MCP server unreachable | A reconnect or background connect attempt failed and the server is in cooldown | server name |
| High | MCP host server stopped | ClawEh's own MCP server died after startup | `mcpserver` |
| High | HTTP listener stopped | The WebUI/API listener failed to bind or died | `http` |
| High | Agent loop stopped | The agent loop returned an error | `agent-loop` |
| High | Channel failed to start | A channel exhausted its start retries | channel name |
| Low | Channel send failed | An outbound message was dropped after its send retries | channel name |
| High | Channel receive loop stopped | Slack, Matrix or the device gateway stopped receiving while still reporting running | channel name |
| High | Telegram polling failed | The long poll fails with 401 (token revoked) or 409 (another poller) | channel name |
| High | SecMsg account discovery failed | The SecMsg daemon could not be queried; no accounts bound until the next reload | `SecMsg (<name>)` |
| High | SecMsg has no linked accounts | The daemon has no account to bind | `SecMsg (<name>)` |
| Low | Scheduled job failed | A cron job's handler returned an error, or the job could not be delivered | job id |
| High | Cron store unreadable | `jobs.json` could not be read at startup | `cron-store` |
| Low | Cron store not saved | A job-state write failed | `cron-store` |
| High | Session not saved | A conversation could not be written to its session store | `session-store` |
| High | Service tokens not loaded | The service-token file could not be read | `service-tokens` |
| High | Config file invalid | A config edit on disk could not be loaded or validated and was not applied | `config` |
| High | Config reload failed | Applying a valid config failed part way; services may not all be running | `config` |
| High | Nightly backup failed | The configuration backup did not run | `backup` |
| High | Log rotation failed | The midnight log roll failed; file logging may be stopped | `logging` |
| High | Fusion token store unavailable | The Google/Microsoft token database could not be opened; all Fusion tools disabled | `fusion` |
| High | Maestro tools disabled | An agent's Maestro directory could not be prepared | `maestro:<agent>` |
| High | Cognitive memory migration failed | An agent's memory database could not be opened or migrated | `cogmem:<agent>` |
| High | Message-token store unreadable | An agent's token file is corrupt | `msgtoken:<agent>` |
| High | Message-token store not written | A token change (including a revocation) could not be saved | `msgtoken:<agent>` |
| High | Named message-token store unreadable | The named-token file could not be loaded | `msgtoken:named` |
| Low | Session state not persisted | A per-session setting could not be written | `session-store` |
| Low | Sub-agent record not written | A sub-agent status or results file could not be written | `subagent-store` |
| Low | Mount marker not written | A watched mount's seen-files marker could not be written | `mount:<path>` |
| High | Voice transcription rejected | The transcription API answered 401, 402 or 403 | `voice:<provider>` |
| Low | Device source not started | A device event source failed to start | `devices:<kind>` |
| Low | USB device monitor stopped | The udevadm monitor stream ended | `devices:usb` |
| Low | Device store unavailable | The paired-device database could not be opened for a request | `device-store` |

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
  raises "Session archive not written" (high, id `<agent>`).
- cogmem: `WithRunErrorHook(func(RunError))` on the consolidation manager with
  `Job`, `Trigger`, `Stage` (factory or run), `Status`, `Err`. ClawEh raises
  "Memory consolidation failed" (low, id `<agent>`; retried on the next
  trigger, repeats collapse).
- MCPFusion: a typed `RefreshError{StatusCode, Body}` from the strategies so
  a revoked token (400/401) can be told from a transient failure, and
  `WithAuthEventHook(func(AuthEvent))` on the Fusion engine with `Kind`
  (refresh failed, client reported error), `Tenant`, `Service`, `AuthType`,
  `StatusCode`, `Message`, `Err`. ClawEh raises "OAuth token refresh rejected"
  (high, id `<agent>/<service>`) and "OAuth client reported error".

Each is a minor version bump of its module, then a `go get` in ClawEh and four
rows in `ALERTS.md`.
