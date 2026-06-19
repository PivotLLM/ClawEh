# Cron — Scheduled Jobs

Cron lets agents (and you, via the CLI) schedule tasks to run at a specific time, on a
recurring interval, or on a cron expression. Jobs are stored in
`~/.claw/cron/jobs.json` and survive restarts.

---

## How jobs run

A job is **addressed to an agent** (recorded as `agentId`). When it fires, its `message`
is injected **inbound** to that agent's **default channel** — exactly as if a user had
sent that message there. Normal routing delivers it to the agent, which processes it and
replies on the same channel. The agent that schedules a job cannot choose a different
destination.

The destination is **resolved live from the target agent's default channel at the moment
the job fires** — not captured when the job is created. So if you change which channel is
the agent's default, existing jobs follow automatically. The default channel is the
binding marked **default** for that agent (see below); it must resolve to a concrete chat
(a channel + a concrete peer). An agent with no default channel cannot schedule jobs (the
`add` is rejected) and a job whose agent loses its default channel is skipped at fire time
with a warning.

Because a job only needs the **agent name** (not a live conversation), agents reach
`cron_schedule add` over any transport, including external MCP clients (e.g. Claude Code)
where there is no inbound channel.

### Default channel

Each agent's reachable channels are its **bindings**. Exactly one binding per agent may be
marked `default: true`, and it is where that agent's cron output (and other
agent-targeted delivery) is sent. Set it in the WebUI agent view ("Channels"), or in
config:

```json
{ "agent_id": "karen", "default": true,
  "match": { "channel": "slack", "peer": { "kind": "channel", "id": "C0…" } } }
```

### Scheduling for another agent (`global_cron`)

By default an agent schedules and manages **only its own** jobs. An agent with
`"global_cron": true` in its config may create and manage jobs for **any** agent by
passing the `agent` parameter (`cron_schedule` → `{"action":"add","agent":"karen",…}`).
Typically a single orchestrator agent has this. Without it, targeting another agent is
rejected.

### Scope (per agent)

Through the `cron_schedule` tool an agent sees and manages **only its own** jobs —
`list`/`get`/`remove`/`enable`/`disable` ignore other agents' jobs (a `get`/etc. on
someone else's id returns "not found") — unless it has `global_cron` and passes the
`agent` parameter. The `claw cron` **CLI is the operator view and sees all jobs**
regardless of owner.

### Loading a domain's context when a job fires

A scheduled message is processed like any other, so the agent's cognitive-memory
routing applies. To make a **workflow/domain load when the job fires**, give that
domain **keyword triggers** (`cogmem_domain_update` → `set_keyword_triggers`) and
word the cron message to contain one of those phrases. For example, a domain with
`["morning routine"]` is pulled into context when the cron message says "run the
morning routine." (Domain *tool* triggers don't help here — they match tool names,
not the message; keyword triggers match the message text.)

### Execution model

Jobs are placed on the same inbound message queue as messages from users and processed in
order — if the agent is already handling a message when a job fires, the job waits its
turn. This prevents a job and a live user message from running concurrently and stepping
on each other's shared session state.

> **Note:** earlier versions had per-job execution `mode`s (`agent`, `isolated`,
> `deliver`, `command`) and a `command` field for running shell commands. These were
> removed — every job now uses the single inbound behavior above. Legacy `mode`/`command`
> fields left in `jobs.json` are ignored.

---

## Job fields

### Top-level

| Field | Type | Description |
|---|---|---|
| `id` | string | Unique job ID, generated automatically |
| `name` | string | Human-readable label shown in `claw cron list` |
| `enabled` | bool | `true` to run on schedule, `false` to pause |
| `schedule` | object | When to run — see [Schedule](#schedule) |
| `payload` | object | What to do when the job fires — see [Payload](#payload) |
| `state` | object | Runtime state (next/last run, status) — managed automatically |
| `createdAtMs` | int | Unix timestamp (ms) when the job was created |
| `updatedAtMs` | int | Unix timestamp (ms) of last modification |
| `deleteAfterRun` | bool | If `true`, the job is deleted after it runs once (set automatically for `at` schedules) |

---

### Schedule

| Field | Type | Description |
|---|---|---|
| `kind` | string | `"cron"`, `"every"`, or `"at"` |
| `expr` | string | Cron expression — used when `kind` is `"cron"` (e.g. `"0 8 * * *"` for 8am daily) |
| `everyMs` | int | Interval in milliseconds — used when `kind` is `"every"` (e.g. `3600000` for every hour) |
| `atMs` | int | One-time fire time as Unix timestamp (ms) — used when `kind` is `"at"` |
| `tz` | string | Timezone for cron expressions (e.g. `"America/Toronto"`). Omit to use system timezone. |

**Examples:**

```json
{ "kind": "cron", "expr": "0 8 * * *" }          // every day at 8am
{ "kind": "cron", "expr": "0 9 * * 1-5" }         // weekdays at 9am
{ "kind": "every", "everyMs": 3600000 }            // every hour
{ "kind": "at", "atMs": 1775217600000 }            // one-time, specific time
```

---

### Payload

| Field | Type | Description |
|---|---|---|
| `message` | string | The message injected inbound when the job fires |
| `channel` | string | Channel to deliver to (e.g. `"slack"`, `"telegram-Amber"`) |
| `to` | string | Channel or user ID to deliver to (e.g. a Slack channel ID `C0ABC123` or user ID `U0ABC123`) |
| `peer_kind` | string | `"channel"` (default) or `"direct"` — see [peer_kind](#peer_kind) |

> Legacy payloads may also contain `mode` and `command`; both are ignored.

---

## `peer_kind`

Tells the router whether `to` is a **channel ID** or a **DM user ID**. It determines which
session key the fired message routes to — and that key must match what the user's reply
produces, otherwise the agent won't remember the conversation.

| Value | Use when `to` is... | Example |
|---|---|---|
| `channel` | A group channel or room ID | Slack `C0AMNPSSQRK` |
| `direct` | A DM user ID | Slack `U0YOURSLACKID` |

If you get this wrong, the cron-fired session and your reply session won't match — the
agent will respond without memory of what it sent you.

**Rule of thumb:**
- Slack channel IDs start with `C` → use `"channel"`
- Slack user IDs start with `U` → use `"direct"`
- Telegram chat IDs for groups are negative numbers → use `"channel"`
- Telegram user IDs for DMs are positive numbers → use `"direct"`

---

## Session behaviour

The fired message routes to the real user session derived from `channel` + `to` +
`peer_kind` (the same session a live message from there would use), so replies continue
the conversation. Jobs are queued on the inbound bus and processed in order.

---

## CLI usage

Jobs can be managed with the `claw cron` command or created conversationally by the agent.

### List jobs

```
claw cron list
```

### Add a job

```
claw cron add \
  --name "Daily standup reminder" \
  --message "Time for your standup!" \
  --cron "0 9 * * 1-5" \
  --channel slack \
  --to C0ANLEQP5GQ
```

**Flags:**

| Flag | Default | Description |
|---|---|---|
| `--name` / `-n` | required | Job name |
| `--message` / `-m` | required | Message injected when the job fires |
| `--cron` / `-c` | | Cron expression |
| `--every` / `-e` | | Recurring interval in seconds |
| `--peer-kind` | `channel` | `channel` or `direct` |
| `--channel` | | Channel to deliver to |
| `--to` | | Channel or user ID |

`--cron` and `--every` are mutually exclusive.

### Remove a job

```
claw cron remove --job-id <id>
```

### Enable / disable a job

```
claw cron enable --job-id <id>
claw cron disable --job-id <id>
```

---

## Conversational usage

When chatting with an agent, you can ask it to schedule tasks directly:

> "Remind me every day at 9am to check my email"

> "In 30 minutes, send me a summary of what we discussed"

> "Every Monday at 8am, run the weekly report playbook"

The agent will call the `cron` tool and confirm the job was created. Use `claw cron list`
to see all scheduled jobs, or ask the agent to list them.

---

## Example jobs.json

```json
{
  "version": 1,
  "jobs": [
    {
      "id": "bf5520e6afece543",
      "name": "Morning greeting",
      "enabled": true,
      "schedule": {
        "kind": "cron",
        "expr": "0 9 * * *"
      },
      "payload": {
        "message": "Good morning!",
        "channel": "slack",
        "to": "C0ANLEQP5GQ",
        "peer_kind": "channel"
      },
      "state": {
        "nextRunAtMs": 1775221200000,
        "lastRunAtMs": 1775134800156,
        "lastStatus": "ok"
      },
      "createdAtMs": 1774045644536,
      "updatedAtMs": 1775134800156,
      "deleteAfterRun": false
    },
    {
      "id": "6dc3718f0b9dc996",
      "name": "Daily email summary",
      "enabled": true,
      "schedule": {
        "kind": "cron",
        "expr": "0 8 * * *"
      },
      "payload": {
        "message": "Get email.md from the assistant playbook and run it.",
        "channel": "slack",
        "to": "C0AMNPSSQRK",
        "peer_kind": "channel"
      },
      "state": {
        "nextRunAtMs": 1775217600000,
        "lastRunAtMs": 1775131200156,
        "lastStatus": "ok"
      },
      "createdAtMs": 1774045644545,
      "updatedAtMs": 1775131725674,
      "deleteAfterRun": false
    }
  ]
}
```
