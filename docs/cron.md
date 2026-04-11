# Cron — Scheduled Jobs

Cron lets agents (and you, via the CLI) schedule tasks to run at a specific time, on a
recurring interval, or on a cron expression. Jobs are stored in
`~/.claw/cron/jobs.json` and survive restarts.

---

## How jobs run

When a job fires, the cron service calls the agent with the job's payload. What happens
next depends on the job's `mode` (see below). The agent sends its response to the
`channel` and `to` specified in the payload.

### Execution model

`agent` mode jobs are placed on the same inbound message queue as messages from users.
They are processed in order — if the agent is already handling a message when the job
fires, the job waits its turn. This prevents two `agent`-mode jobs (or a job and a live
user message) from running concurrently and stepping on each other's shared session state.

`isolated` mode jobs bypass the queue and run immediately in their own goroutine. Because
each isolated job uses a unique session key (`cron-{jobID}`) that is never shared with
any other message, there is no risk of state collision regardless of what else is running.

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
| `mode` | string | How the job executes — see [Modes](#modes) |
| `message` | string | The message or prompt to send |
| `command` | string | Shell command to run (only used with `mode: "command"`) |
| `channel` | string | Channel platform to deliver to (e.g. `"slack"`, `"telegram"`) |
| `to` | string | Channel or user ID to deliver to (e.g. a Slack channel ID `C0ABC123` or user ID `U0ABC123`) |
| `peer_kind` | string | `"channel"` (default) or `"direct"` — see [peer_kind](#peer_kind) |

---

## Modes

`mode` controls what happens when the job fires.

### `agent` (default)

The message is fed to the agent as a prompt. The agent processes it in the **user's real
ongoing session** — the same session the user converses in. Replies from the user continue
the conversation naturally; the agent remembers what it sent.

Use this for tasks where follow-up is expected — summaries, reports, questions.

```json
{
  "mode": "agent",
  "message": "Get email.md from the assistant playbook and run it.",
  "channel": "slack",
  "to": "C0AMNPSSQRK",
  "peer_kind": "channel"
}
```

### `isolated`

The message is fed to the agent in a **fresh, one-off session**. No memory of previous
runs or conversations. Each run starts clean.

Use this for recurring tasks where you don't want context from previous runs (e.g. a
nightly system health check that should always start fresh).

```json
{
  "mode": "isolated",
  "message": "Check disk usage and memory. Report anything above 80%.",
  "channel": "slack",
  "to": "C0AMNPSSQRK",
  "peer_kind": "channel"
}
```

### `deliver`

The message is sent **verbatim** to the channel. No LLM is involved. The text arrives
exactly as written.

Use this for simple scheduled notifications — a morning greeting, a reminder, a static
message at a fixed time.

```json
{
  "mode": "deliver",
  "message": "Good morning! Don't forget your 10am standup.",
  "channel": "slack",
  "to": "C0ANLEQP5GQ",
  "peer_kind": "channel"
}
```

### `command`

Runs a shell command and sends the output to the channel. The `command` field holds the
shell command; `message` is a human-readable description used as the job name.

**Security:** Command jobs can only be created from an internal channel (e.g. the CLI),
not from Slack or Telegram.

```json
{
  "mode": "command",
  "message": "Daily disk usage report",
  "command": "df -h",
  "channel": "slack",
  "to": "C0AMNPSSQRK",
  "peer_kind": "channel"
}
```

---

## `peer_kind`

Tells the router whether `to` is a **channel ID** or a **DM user ID**. This matters for
`agent` mode because it determines which session key is used — and the session key must
match what the user's reply will produce, otherwise the agent won't remember the
conversation.

| Value | Use when `to` is... | Example |
|---|---|---|
| `channel` | A group channel or room ID | Slack `C0AMNPSSQRK` |
| `direct` | A DM user ID | Slack `U0YOURSLACKID` |

If you get this wrong with `mode: "agent"`, the agent's cron session and your reply session
won't match — the agent will respond without memory of what it sent you.

**Rule of thumb:**
- Slack channel IDs start with `C` → use `"channel"`
- Slack user IDs start with `U` → use `"direct"`
- Telegram chat IDs for groups are negative numbers → use `"channel"`
- Telegram user IDs for DMs are positive numbers → use `"direct"`

---

## Session behaviour

| Mode | Session | Execution | Effect on replies |
|---|---|---|---|
| `agent` | Real user session (derived from `channel` + `to` + `peer_kind`) | Queued | Replies continue the conversation |
| `isolated` | Isolated session (`cron-{jobID}`) | Immediate (goroutine) | Replies start a fresh conversation |
| `deliver` | None | Immediate | No session |
| `command` | None | Immediate | No session |

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
  --mode deliver \
  --channel slack \
  --to C0ANLEQP5GQ
```

**Flags:**

| Flag | Default | Description |
|---|---|---|
| `--name` / `-n` | required | Job name |
| `--message` / `-m` | required | Message or prompt |
| `--cron` / `-c` | | Cron expression |
| `--every` / `-e` | | Recurring interval in seconds |
| `--mode` | `agent` | Execution mode: `agent`, `isolated`, `deliver`, `command` |
| `--peer-kind` | `channel` | `channel` or `direct` |
| `--channel` | | Channel platform |
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
        "mode": "deliver",
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
        "mode": "agent",
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
