# Maestro

Maestro gives an agent a project manager's toolkit for large, multi-step work: projects, item lists, task sets run by worker sub-agents, reusable playbooks and generated reports.

## What it is for

Without Maestro, an agent works through a job inside one conversation, and
a long job (check 200 requirements, review every file in a repository)
outgrows it. With Maestro the agent can:

- break the job into tasks and record them on disk;
- hand each task to a fresh worker, several at once if allowed;
- check each result with an automatic quality-assurance step and retry
  failures;
- resume where it stopped, in a later conversation or after a restart;
- combine the results into a report.

Good uses: security or compliance reviews against a standard, document
reviews against a checklist, repository analysis, research, and any job you
will repeat and want done the same way each time. For a quick question or a
one-off task, Maestro is unnecessary.

You do not use Maestro directly. You ask the agent ("use Maestro to review
every control in `files/iso27001-controls.md`"), and it uses the Maestro
tools. Its first step is usually to read the built-in guide
(`maestro_start_here`).

## Enable it for an agent

Maestro is per agent and off by default.

1. Open the **Agents** page.
2. On the agent's card, turn on **Maestro**.
3. Optionally adjust the settings that appear below the switch:

   | Setting | Default | Meaning |
   |---|---|---|
   | Max concurrent tasks | 5 | Tasks executed at once in a parallel run. |
   | Rate limit | 10 dispatches per 60 seconds | At most this many tasks handed to workers per period. |
   | **Allow parallel runs** | on | Runs are one task at a time unless the agent asks for parallel; turn this off to refuse such requests. |

Changes are saved automatically. The agent then has the whole Maestro tool
suite (tools named `maestro_...`). It cannot be granted tool by tool, but
**Denied tools** can remove individual Maestro tools.

In `config.json` the setting is an object on the agent:

```json
{
  "id": "alice",
  "maestro": {
    "enabled": true,
    "max_concurrent": 5,
    "rate_limit_requests": 10,
    "rate_limit_period": 60,
    "allow_parallel": true
  }
}
```

The older form `"maestro": true` is not accepted: ClawEh logs a warning and
runs the agent without Maestro until the object form is set.

## The parts, at a glance

| Part | What it is |
|---|---|
| **Project** | One piece of work, such as "Review product X against standard Y". It holds the input files, task sets, results, reports and a log of what was done. Projects can have one level of subprojects. |
| **List** | A structured set of items to process, such as every requirement in a standard. The agent can create one task per item, so nothing is skipped. |
| **Task set and task** | A task is one unit of work ("evaluate requirement 4.2 against the evidence"). Tasks are grouped in task sets, such as `analysis` or `qa`. |
| **Runner** | Executes the tasks: hands each to a worker, applies the quality check, retries, and stores the result. |
| **Playbook** | A saved procedure for a type of work, reusable across projects and improved over time. |
| **Report** | A document built from the stored task results. |
| **Reference** | Read-only material: Maestro's own guide, plus the agent's mounts. |

## How workers run

- Every worker, quality check and revision runs as a **sub-agent of the
  agent**: on the agent's default model, with the agent's tools and
  permissions, in its workspace.
- A task may name another of the agent's models. An unknown model name fails
  the task without a retry.
- Workers count toward the sub-agent depth limit
  (`agents.defaults.max_subagent_depth`, default 3), so a chain of workers
  starting workers cannot run away.
- A worker whose tool calls all failed counts as a failed attempt.

## What Maestro can read

- The agent's **mounts** appear in Maestro's reference material under each
  mount's name.
- When the agent imports a file into a project, it can import only what its
  own file tools may read: its workspace, its mounts and any paths allowed
  for reading. To import from somewhere else, add a mount to the agent.

## Where its data lives

Everything is in the agent's workspace, by default
`<CLAW_HOME>/agents/<agent>/maestro/`:

| Folder | Contents |
|---|---|
| `maestro/projects/` | Projects: their files, task sets, results, reports and logs. |
| `maestro/playbooks/` | Playbooks. |

The same folder is mounted as `maestro/` for the agent's ordinary file
tools, so the agent can move inputs and finished reports between
`maestro/` and `files/`. Project logs and results are never pruned.

The nightly backup does **not** include the `maestro/` folder. Back it up
yourself if the projects matter.

Maestro's operational log goes to the ClawEh log with the component
`maestro`, visible on the **Logs** page.

## Using Maestro from another program

An MCP client outside ClawEh can drive an agent's Maestro tools with that
agent's service token. See [ClawEh as an MCP server](mcp.md#service-tokens).
