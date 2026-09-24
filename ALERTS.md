# ClawEh alerts

Every alert ClawEh raises through `github.com/tenebris-tech/alerter`. The
**alert id** is the alerter's `EventID` field: repeats of the same title with
the same id inside the suppression window (ten minutes) are counted, not
re-sent, so the id names the thing that failed. Where alerts go, and the record
format, are described in `docs/alerts.md`.

| Alert id (`EventID`) | Alert | Comments |
|---|---|---|
| `<provider>/<model>` | Model authentication or billing failure | **High.** Raised on the first `auth` or `billing` failure, before any retry: a CLI logged out, an API key revoked, credit exhausted. The model is parked; no retry fixes it. Description gives the cooldown length, the failure count, the reason and the HTTP status. Source: `providers/cooldown.go`. |
| `<provider>/<model>` | Model parked after repeated failures | **Low.** Raised for any other failure reason (rate limit, timeout, server error) only once the 1/3/5-minute escalation is used up and the model settles into its category cooldown, so one transient error alerts nobody. Same description as above. Source: `providers/cooldown.go`. |
| `<MCP server name>` | MCP server unreachable | **Low.** A reconnect or background connect attempt failed and the server is in cooldown. Its tools are missing until it reconnects; reconnects retry after the cooldown, or use Reconnect on the MCP servers page. Description carries the connect error. Source: `mcp/manager.go`, `mcp/resilience.go`. |
| `mcpserver` | MCP host server stopped | **High.** ClawEh's own MCP server (the one external clients and CLI providers use for host tools) died after startup. Nothing restarts it. Description gives the listen address. Source: `mcpserver/mcpserver.go`. |
| `http` | HTTP listener stopped | **High.** The shared HTTP listener (WebUI, API, health endpoint, channel webhooks) failed to bind or died. Nothing restarts it; the gateway process stays up without it. Description gives the address. Source: `internal/gateway/httphost.go`. |
| `agent-loop` | Agent loop stopped | **High.** The agent loop returned an error and no inbound message is processed until a restart. Reachable only through an MCP initialisation failure today; kept because the impact would be total. Source: `internal/gateway/helpers.go`. |
| `<channel name>` | Channel failed to start | **High.** The channel exhausted its start retries and will not be retried until a restart or config reload. Details carry the last start error. Source: `channels/manager.go`. |
| `<channel name>` | Channel send failed | **Low.** An outbound message was dropped after its send retries (or at once for a permanent error). The channel itself stays up. Details carry the last send error. Source: `channels/manager.go`. |
| `<channel name>` | Channel receive loop stopped | **High.** A running channel stopped receiving: Slack Socket Mode connection error, Matrix sync stopped (a revoked access token stops it for good), device gateway listener error. The channel still reports running; nothing reconnects it until a restart. Details carry the error. Source: `channels/slack`, `channels/matrix`, `channels/device`. |
| `<channel name>` | Telegram polling failed | **High.** The Telegram long poll fails with a non-transient error: 401 Unauthorized (bot token revoked) or 409 Conflict (another poller on the same token). Transient 5xx and network blips are retried and never alert. Repeats every two seconds, collapsed by the alerter. Source: `channels/telegram`. |
| `SecMsg (<name>)` | SecMsg account discovery failed | **High.** The SecMsg daemon could not be asked for its accounts, so the channel bound none; nothing retries before the next config reload. Description gives the daemon address. Source: `channels/manager.go`. |
| `SecMsg (<name>)` | SecMsg has no linked accounts | **High.** The daemon answered but has no linked account; link one in the WebUI. Source: `channels/manager.go`. |
| `<cron job id>` | Scheduled job failed | **Low.** A cron job did not run: the handler returned an error, the agent has no default channel, an operator job lacks channel/to, the configuration is not loaded, or the message could not be queued. The job's state records the error and the next run is still scheduled. Source: `cron/service.go`, `tools/schedule/cron.go`. |
| `cron-store` | Cron store unreadable | **High.** `jobs.json` could not be read or parsed at startup, so no scheduled jobs run and the next save overwrites the file. Fix or restore the file before anything writes it. Source: `cron/service.go`, `internal/gateway/helpers.go`. |
| `cron-store` | Cron store not saved | **Low.** A job-state write failed (disk full, permissions). Jobs keep running from memory and drift from disk; changes are lost on restart. Source: `cron/service.go`. |
| `session-store` | Session not saved | **High.** A conversation could not be written to its session store, so history is being lost. One id for every session, so a full disk produces one alert. Source: `agent/loop_turn.go`. |
| `service-tokens` | Service tokens not loaded | **High.** The service-token file could not be read: external MCP clients using `claw token` credentials are rejected (at boot) or keep the previously loaded set (on a later change). Source: `internal/gateway/helpers.go`. |
| `config` | Config file invalid | **High.** The config file on disk changed but could not be loaded or validated, so the edit was not applied. The running configuration is unchanged, but the next restart fails on this file. Source: `internal/gateway/helpers.go`. |
| `config` | Config reload failed | **High.** A valid config was accepted but applying it failed part way. Services are stopped before a reload, so some may not be running; check the gateway log. Details carry the reload error. Source: `internal/gateway/helpers.go`. |
| `backup` | Nightly backup failed | **High.** The configuration backup did not run and nothing retries before the next night; usually disk or permissions. Source: `internal/gateway/backup.go`. |
| `logging` | Log rotation failed | **High.** The midnight log roll failed after closing the current files, so file logging may be stopped until a restart. Repeats daily. Source: `internal/gateway/logrotate.go`. |

If the alerts log itself cannot be opened, ClawEh logs an error in the gateway
log and runs without alerting; it cannot alert about that.

Known conditions that are **not** alerted yet, because they surface only
inside shared modules that have no alert hook: session archive open/append
failures in ctxengine, cognitive-memory consolidation failures in cogmem, and
Google/Microsoft OAuth refresh failures in MCPFusion. Each needs an error
callback in its module first.

Titles are fixed strings, so the alert log can be searched by them. The
priority decides how a future delivery channel treats the alert: high is for
conditions that need a person (nothing in ClawEh will fix them), low for
conditions ClawEh keeps retrying or working around.
