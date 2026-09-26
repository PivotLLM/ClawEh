# ClawEh alerts

Every alert ClawEh raises through `github.com/tenebris-tech/alerter`. The
**alert id** is the alerter's `EventID` field: repeats of the same title with
the same id inside the suppression window (ten minutes) are counted, not
re-sent, so the id names the thing that failed. Where alerts go, and the record
format, are described in `docs/alerts.md`.

**Priority.** The alerter has three levels: Normal, Urgent and Emergency.
Every ClawEh alert is Normal: written to the log and delivered on the
configured channels at their normal priority. Urgent and Emergency are for
something that must reach a person now, any hour of the day; none of
ClawEh's alerts qualifies today. The Priority column is blank for Normal,
`*` for Urgent and `**` for Emergency.

| Alert id (`EventID`) | Priority | Alert | Comments |
|---|---|---|---|
| `<provider>/<model>` | | Model authentication or billing failure | Raised on the first `auth` or `billing` failure, before any retry: a CLI logged out, an API key revoked, credit exhausted. The model is parked; no retry fixes it. Description gives the cooldown length, the failure count, the reason and the HTTP status. Source: `providers/cooldown.go`. |
| `<provider>/<model>` | | Model parked after repeated failures | Raised for any other failure reason (rate limit, timeout, server error) only once the 1/3/5-minute escalation is used up and the model settles into its category cooldown, so one transient error alerts nobody. Same description as above. Source: `providers/cooldown.go`. |
| `<MCP server name>` | | MCP server unreachable | A reconnect or background connect attempt failed and the server is in cooldown. Its tools are missing until it reconnects; reconnects retry after the cooldown, or use Reconnect on the MCP servers page. Description carries the connect error. Source: `mcp/manager.go`, `mcp/resilience.go`. |
| `mcpserver` | | MCP host server stopped | ClawEh's own MCP server (the one external clients and CLI providers use for host tools) died after startup. Nothing restarts it. Description gives the listen address. Source: `mcpserver/mcpserver.go`. |
| `http` | | HTTP listener stopped | The shared HTTP listener (WebUI, API, health endpoint, channel webhooks) failed to bind or died. Nothing restarts it; the gateway process stays up without it. Description gives the address. Source: `internal/gateway/httphost.go`. |
| `agent-loop` | | Agent loop stopped | The agent loop returned an error and no inbound message is processed until a restart. Reachable only through an MCP initialisation failure today; kept because the impact would be total. Source: `internal/gateway/helpers.go`. |
| `<channel name>` | | Channel failed to start | The channel exhausted its start retries and will not be retried until a restart or config reload. Details carry the last start error. Source: `channels/manager.go`. |
| `<channel name>` | | Channel send failed | An outbound message was dropped after its send retries (or at once for a permanent error). The channel itself stays up. Details carry the last send error. Source: `channels/manager.go`. |
| `<channel name>` | | Channel receive loop stopped | The device gateway listener failed. The channel still reports running; nothing restarts the listener until a restart. Details carry the error. Source: `channels/device`. |
| `<channel name>` | | Channel connection down | A channel has had no working connection to its service for `ConnDownAlertAfter` (ten minutes, in `channels/tuning.go`) although it kept retrying: Telegram polls failing (5xx, 429, network, 409 from another poller), Slack Socket Mode or Matrix sync not reconnecting, Discord gateway disconnected, SecMsg daemon unreachable. Raised once per outage; retries continue, and recovery is logged as "Channel connection restored". Details carry the latest error. Source: `channels/connwatch.go`. |
| `<channel name>` | | Channel credentials rejected | Slack rejected the app token (`invalid_auth`, `token_revoked`, `account_inactive`, `not_authed`) or the Matrix homeserver answered `M_UNKNOWN_TOKEN`. No retry fixes it; the channel keeps retrying every minute in case the token is restored. Details carry the error. Source: `channels/slack`, `channels/matrix`. |
| `<channel name>` | | Telegram polling failed | Telegram answered the long poll with 401 Unauthorized: the bot token was revoked or is wrong. No retry fixes it; the poll keeps retrying every minute, and repeats collapse in the alerter. Every other poll failure is retried and counts toward "Channel connection down" instead. Source: `channels/telegram`. |
| `SecMsg (<name>)` | | SecMsg account discovery failed | The SecMsg daemon could not be asked for its accounts, so the channel bound none; nothing retries before the next config reload. Description gives the daemon address. Source: `channels/manager.go`. |
| `SecMsg (<name>)` | | SecMsg has no linked accounts | The daemon answered but has no linked account; link one in the WebUI. Source: `channels/manager.go`. |
| `<cron job id>` | | Scheduled job failed | A cron job did not run: the handler returned an error, the agent has no default channel, an operator job lacks channel/to, the configuration is not loaded, or the message could not be queued. The job's state records the error and the next run is still scheduled. Source: `cron/service.go`, `tools/schedule/cron.go`. |
| `cron-store` | | Cron store unreadable | `jobs.json` could not be read or parsed at startup, so no scheduled jobs run and the next save overwrites the file. Fix or restore the file before anything writes it. Source: `cron/service.go`, `internal/gateway/helpers.go`. |
| `cron-store` | | Cron store not saved | A job-state write failed (disk full, permissions). Jobs keep running from memory and drift from disk; changes are lost on restart. Source: `cron/service.go`. |
| `session-store` | | Session not saved | A conversation could not be written to its session store, so history is being lost. One id for every session, so a full disk produces one alert. Source: `agent/loop_turn.go`. |
| `service-tokens` | | Service tokens not loaded | The service-token file could not be read: external MCP clients using `claw token` credentials are rejected (at boot) or keep the previously loaded set (on a later change). Source: `internal/gateway/helpers.go`. |
| `config` | | Config file invalid | The config file on disk changed but could not be loaded or validated, so the edit was not applied. The running configuration is unchanged, but the next restart fails on this file. Source: `internal/gateway/helpers.go`. |
| `config` | | Config reload failed | A valid config was accepted but applying it failed part way. Services are stopped before a reload, so some may not be running; check the gateway log. Details carry the reload error. Source: `internal/gateway/helpers.go`. |
| `backup` | | Nightly backup failed | The configuration backup did not run and nothing retries before the next night; usually disk or permissions. Source: `internal/gateway/backup.go`. |
| `fusion` | | Fusion token store unavailable | The Fusion (Google, Microsoft) token database could not be opened when the first agent registered its tools, so every Fusion tool is disabled for the life of the process. Source: `tools/fusion/engine.go`. |
| `maestro:<agent>` | | Maestro tools disabled | The agent has Maestro enabled but its `maestro/` directory could not be created or prepared, so it has no Maestro tools. Raised on every registry build; repeats per agent collapse. Source: `tools/maestro/global_provider.go`. |
| `cogmem:<agent>` | | Cognitive memory migration failed | The agent's memory database could not be opened or migrated at load, so its memory is unavailable or stale. Source: `cogmemhost/paths.go`. |
| `msgtoken:<agent>` | | Message-token store unreadable | The agent's message-token file is corrupt: existing tokens are ignored and the next save overwrites it. Source: `msgtoken/manager.go`. |
| `msgtoken:<agent>` | | Message-token store not written | A token change could not be saved. A revocation that does not reach disk means the token comes back after a restart. Source: `msgtoken/named.go`, `msgtoken/manager.go`. |
| `msgtoken:named` | | Named message-token store unreadable | The named-token file could not be loaded at startup; named tokens do not work and the next change overwrites the file. Source: `agent/loop.go`. |
| `session-store` | | Session state not persisted | A per-session setting (active model, expose reasoning, show tool activity) could not be written and reverts on restart. Same id as "Session not saved" so a full disk stays one alert. Source: `agent/loop_session_state.go`. |
| `subagent-store` | | Sub-agent record not written | A sub-agent task's status or results file could not be written, so it cannot be resumed or reported. Source: `tools/agents/subagent.go`. |
| `mount:<path>` | | Mount marker not written | The seen-files marker for a watched mount could not be written, so the same files may be reported again. Source: `mountwatch/watch.go`. |
| `voice:<provider>` | | Voice transcription rejected | The transcription API answered 401, 402 or 403 (key or credit), which persists across requests; voice messages are not transcribed until it is fixed. Other statuses and I/O errors never alert. Source: `voice/transcriber.go`. |
| `devices:<kind>` | | Device source not started | A device event source failed to start (for example `udevadm` missing); events from it are unavailable. Source: `devices/service.go`. |
| `devices:usb` | | USB device monitor stopped | The `udevadm monitor` stream ended with an error and is not restarted. Source: `devices/sources/usb_linux.go`. |
| `device-store` | | Device store unavailable | The paired-device database could not be opened for a WebUI request; device pages and pairing fail until it can be. Source: `web/backend/api/devices.go`. |
| `logging` | | Log rotation failed | The midnight log roll failed after closing the current files, so file logging may be stopped until a restart. Repeats daily. Source: `internal/gateway/logrotate.go`. |

If the alerts log itself cannot be opened, ClawEh logs an error in the gateway
log and runs without alerting; it cannot alert about that.

Known conditions that are **not** alerted yet, because they surface only
inside shared modules that have no alert hook: session archive open/append
failures in ctxengine, cognitive-memory consolidation failures in cogmem, and
Google/Microsoft OAuth refresh failures in MCPFusion. The agreed design is a
typed error-event hook per module (`WithArchiveErrorHook`, `WithRunErrorHook`,
`WithAuthEventHook`), with ClawEh writing the alert in the hook body; see
"Shared modules" in `docs/alerts.md`. That is a separate project.

## Where the alerter comes from

The gateway builds one alerter and hands it explicitly to what it constructs
(agent loop, channels, cron, MCP manager and host server, listeners, backup,
log rotation, mount watcher, devices, voice, the API handler). Code with no
owner, such as package singletons, free functions and providers rebuilt per
tool registry, raises through the process default in the `alerts` package,
which the gateway sets once at startup. Tests capture the default with
`internal/testalerts.Install`.

Titles are fixed strings, so the alert log can be searched by them.
