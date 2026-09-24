# Operator alerts

ClawEh raises an alert when something happens that the operator should hear
about through a path other than the agent's own channels: a model that can
no longer authenticate, a channel that gave up, a scheduled job that failed.
Alerts are produced by the `github.com/tenebris-tech/alerter` module.

## Where alerts go

Version 0.0.x of the module writes a log. ClawEh writes it to
`<CLAW_HOME>/logs/alerts.txt`; set `ALERTER_LOG` (full path and file name) in
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

| Priority | Alert | When | Event id |
|---|---|---|---|
| High | Model authentication or billing failure | A model is parked for an `auth` or `billing` failure (a CLI logged out, a key revoked, credit exhausted). Reported on the first failure; no retry fixes it | provider/model |
| Low | Model parked after repeated failures | A model reaches the settled category cooldown after the 1/3/5-minute escalation | provider/model |
| Low | MCP server unreachable | A reconnect or background connect attempt failed and the server is in cooldown | server name |
| High | Channel failed to start | A channel exhausted its start retries | channel name |
| Low | Channel send failed | An outbound message was dropped after its send retries | channel name |
| Low | Scheduled job failed | A cron job's handler returned an error | job id |
| High | Config reload failed | A config reload failed; the previous configuration stays in effect | |

## Repeats

The same title with the same event id inside ten minutes is counted rather
than written again; the next record after the window says how many were
suppressed. A model that is logged out therefore produces one line, not one
per turn.
