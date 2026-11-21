# Background shell: design for a later phase

Status: **design agreed, nothing implemented.** Written with
`refactor.md` in the repository root (its section 4.11 points here). It is self-contained:
a worker can build from this file without the conversation behind it.
Verified against ClawEh 0.6.5.

## 1. The problem

`shell_exec` runs one command and waits for it. Compared with other agent
toolchains it cannot start something, carry on, and come back to see how it
is doing.

What it does today (`tools/shell/shell.go`, `shell_process_unix.go`):

- runs `sh -c <command>` (PowerShell on Windows) in the workspace or a given
  `working_dir`, with the allowlisted child environment
  (`internal/childenv`) and its own process group;
- is killed, whole process group, at `tools.exec.timeout_seconds`
  (60 s when unset) or the per-tool budget
  (`agents.defaults.tool_timeout`), whichever comes first;
- buffers stdout and stderr in memory and returns them, cut to the **first**
  10,000 characters, so the end of a long build log (where the error
  usually is) is what gets lost;
- keeps nothing between calls, not even the working directory;
- has no background mode, no input to a running program, no terminal.

Its arguments are `command` and `working_dir`. It refuses commands only by
the deny, allow and custom-allow patterns (`guardCommand`).

## 2. What is being built

**Background jobs, with one new tool.** Two tools in total:

| Tool | Status | Purpose |
| --- | --- | --- |
| `shell_exec` | existing, extended | run a command, in the foreground or as a background job |
| `shell_job` | new | list, wait on, send input to, or stop a background job |

The tool count is deliberate. Every tool is described to the model on every
request, so the design follows the pattern Claude Code uses for its
execution tool: background is a flag on the one tool, output is a file read
with the ordinary file tools, and the end of a job is announced to the
agent, not polled for.

Agreed with the maintainer: `shell_exec` and exactly one other `shell_`
tool. Its name, `shell_job`, is the working choice: it names the thing the
tool acts on. `shell_control` and `shell_status` were considered; `status`
undersells a tool that also stops jobs and sends input, and `control` hides
that it is also how you look at them. Confirm the name before the first
release that includes it; it is one constant.

Out of scope for the first version (section 8): a persistent interactive
shell, and a terminal (PTY).

## 3. Tool definitions

### 3.1 `shell_exec`

Existing arguments are unchanged. New ones are optional, so every current
call keeps working.

| Argument | Type | Meaning |
| --- | --- | --- |
| `command` | string, required | as now |
| `working_dir` | string | as now. When omitted, the directory the agent's previous `shell_exec` in this conversation ended in (section 4.6), else the workspace |
| `timeout_seconds` | integer | how long a foreground command may run. Default and maximum come from config (section 6) |
| `background` | boolean | start the command as a job and return at once |
| `notify` | boolean | for a job: tell the agent when it ends. Default true |

Results:

- **Foreground, finished in time:** output and exit code, as now, except
  that long output is returned as its head and its tail with the middle
  elided, and names the file holding the whole of it.
- **Foreground, reached its timeout:** the command is **not killed**. It is
  kept as a job, and the result says so: the job id, the output so far
  (head and tail), the output file, and that `shell_job` can wait on it or
  stop it. This replaces today's "Command timed out after 60s" (decided,
  see 7.2).
- **Background:** returns immediately with the job id and the path of the
  output file.

### 3.2 `shell_job`

One tool with an `action`, in the style of the existing `ccv3` tool.

| Argument | Type | Used by |
| --- | --- | --- |
| `action` | `list` \| `wait` \| `input` \| `stop`, required | all |
| `id` | string | `wait`, `input`, `stop` |
| `seconds` | integer | `wait`: how long to block. Default 30, capped by the per-tool budget |
| `until` | string | `wait`: return as soon as a new output line matches this pattern |
| `text` | string | `input`: written to the job's standard input; a newline is added unless `raw` is true |
| `raw` | boolean | `input` |
| `all` | boolean | `stop`: every running job of this agent |

- **`list`**: this agent's jobs: id, command (shortened), state
  (`running`, `exited`, `stopped`, `lost`), exit code, started, ended,
  output size, output file.
- **`wait`**: blocks until the job exits, a line matches `until`, or
  `seconds` pass, whichever is first, then returns why it returned, the
  state, and the output produced since this agent's last `wait` or the
  job's start. A job that already ended returns at once.
- **`input`**: writes to standard input. Refused for a job that has ended.
- **`stop`**: terminate, then kill after a grace period (5 s), the whole
  process group. Idempotent: stopping an ended job is not an error.

There is deliberately **no output-reading action.** The job's log is a
file in the agent's workspace, and `file_read_lines` and
`file_search_lines` read and search it like any other file. `wait` returns
new output only because a caller that just waited always wants it.

### 3.3 The end-of-job notice

When a job ends and `notify` was not turned off, the agent is told once,
as a background result into its main conversation, exactly as a callback
sub-agent's result is delivered: the job id, the command, the exit code or
signal, how long it ran, the last lines of output, and the output file.
It follows the turn-chat rules already in place for background results.

## 4. Behaviour rules

Each of these needs a test.

1. **A job is not tied to the turn that started it.** It runs on its own
   context. The turn ending, or the person interrupting it, does not stop
   the job.
2. **A job is a task.** It is registered in the agent's task list (plan
   section 5), so:
   - `/cancel` leaves jobs running, and its reply counts them among the
     background tasks still running;
   - `/cancel all`, `agent_cancel {"all": true}` and the host's
     `CancelTask` stop them;
   - `shell_job stop` is the shell-specific way to do the same thing.
   If this is built before the task list exists (refactoring phase 5b), it
   keeps its own job list behind the same interface and is folded in then;
   until that point `shell_job stop` and shutdown are the only ways a job
   is stopped.
3. **Ownership.** A job belongs to the agent that started it; a clone acts
   as its source, as for sub-agent tasks. Another agent's job id is
   "unknown".
4. **Output goes to disk as it is produced**, never to memory: one file per
   job, stdout and stderr interleaved in arrival order. When a job's
   output passes its size limit the file keeps the head and the tail and
   records how much was dropped; the job keeps running.
5. **Stopping takes the whole process group**, as the timeout kill does
   today.
6. **Working directory.** After a foreground `shell_exec`, the directory
   the command ended in is remembered for that agent and conversation and
   is the default for the next call. Nothing else carries over: no
   environment variables, no shell functions. This is the one piece of
   state kept, because it removes the most common surprise at almost no
   cost.
7. **Every start goes through the existing guard** (`guardCommand`: deny,
   allow and custom-allow patterns) and gets the allowlisted child
   environment. `input` is not pattern-checked (it is data for a program
   already allowed), but it is audited.
8. **Service restart.** Under systemd the children die with the service.
   At start, any job recorded as running is marked `lost`, the agent is
   told once if `notify` was on, and it is never relaunched: a command is
   not safe to repeat blindly.
9. **Shutdown** stops every job (terminate, grace, kill) before the
   process exits.

## 5. Package layout

Two layers. Neither knows about agents, sessions or ClawEh.

- **`proc`: the process manager.** Standard library only.

  ```go
  m := proc.New(proc.Options{Dir: logDir, Limits: limits, Clock: clk, FileModes: modes})
  job, err := m.Start(ctx, proc.Spec{Owner: "alice", Command: cmd, Dir: cwd, Env: env})
  m.Get(id) (*Job, bool)        m.List(owner string) []Info
  m.Wait(ctx, id, proc.Until{Exit: true, Match: re, Timeout: d}) (Info, []byte, error)
  m.Write(id, data) error       m.Stop(id, grace) error
  m.StopAll(owner string, grace) int
  m.Close(ctx) error            // shutdown: stop everything
  ```

  State is private; callers get copies (`Info`). The manager persists a
  small record per job so `lost` can be detected after a restart.

- **The tools**, a `toolspec.ToolProvider` over a `*proc.Manager`, with the
  guard, the head-and-tail formatting and the notice.

**Where it lives.** In the SDK repository as the first reusable tool
package: `toolbox/shell`, with `proc` inside or beside it. It imports
`toolspec`, `logger`/`slog` and the standard library, with an
`imports_test.go` guard like every SDK package. The agent loop never
imports it; a host adds it to its tool catalogue like any provider.
ClawEh's `tools/shell` becomes a thin mount that supplies the config
(patterns, limits, the working-directory rule, the child environment) and
the notice delivery.

**How it is delivered (decided).** Because it depends only on `toolspec`,
it is built as its own package, independently of the refactoring's
phases, any time after the refactoring starts (the host must be on the
consolidated module, phase 0). When it is ready the host mounts it in
place of the current tool and **the existing `tools/shell` implementation
is removed**; the two are not kept side by side.

## 6. Limits and configuration

These keys are **approved by the maintainer**. All are under `tools.exec`, each `0`/absent =
default, negative refused at load and by `Store.Update`:

| Key | Default | Meaning |
| --- | --- | --- |
| `timeout_seconds` | existing (60) | default foreground timeout |
| `max_timeout_seconds` | 600 | most a call may ask for with `timeout_seconds` |
| `max_jobs` | 8 | running jobs per agent; a start beyond it is refused, naming the running ones |
| `max_job_seconds` | 86400 | a job is stopped after this long |
| `max_job_output_bytes` | 10 MiB | per job; beyond it the middle is dropped |
| `job_log_days` | 7 | finished jobs' logs and records are removed after this |

Logs and records: `<workspace>/state/shell/<job id>.log` and `.json`,
agents-area modes (`perms`), **left out of the backup**, and readable but
not writable by the file tools (add the folder to
`config.AlwaysReadableWorkspaceDirs`, as `forums` and `classifiers` are).

Without the limits a forgetful model fills the disk or leaves servers
running for weeks, so they are not optional.

## 7. Host integration (ClawEh)

### 7.1 Granting

An agent granted `shell_exec` gets `shell_job`; an agent without it gets
neither. `shell_job` is registered together with `shell_exec` and is not a
separate entry in the `tools` list, the Agents page or `CODING_TOOLS`;
`deny_tools` can still remove it. This is the same rule already decided
for `agent_spawn` → `agent_cancel`, and it is shown the same way: on Check
Up's per-agent tool list and in the capabilities golden file. The existing
rule that `*` and prefix entries never include `shell_exec` covers
`shell_job` with it.

### 7.2 Timeout behaviour (decided)

Today a foreground command that runs past its timeout is killed. **Decided:
it is kept as a background job instead**, with no switch to restore the
kill. `max_job_seconds` and `max_jobs` are the backstop against runaway
commands. This is a change on upgrade and gets a `Changed` changelog entry
that says so and names those two keys. An agent **without** `shell_job`
(removed by `deny_tools`) keeps today's kill, since it could never collect
the job.

### 7.3 Visibility and records

- **Audit:** a row for every start (command, working directory,
  background or not), every `input` (length, not content) and every stop,
  with the turn id where there is one. Kinds proposed: `shell_job_start`,
  `shell_job_input`, `shell_job_stop`.
- **Check Up:** a per-agent row for the job tool with the limits, and a
  Data row for the log folders.
- **WebUI:** none (decided). The agent starts a job and the agent
  controls it; there is no operator job list or Stop button. `/cancel all`
  remains the person's way to stop everything an agent has running.
- **Alerts:** none. A job failing is not an outage.
- **MCP host:** both tools are published like any other, so CLI models
  get them; `shell_job wait` must respect the MCP call's deadline.
- **Unattended callers:** classifiers and cron watch probes run tools
  through `ExecuteUnattended`. `shell_job` and `shell_exec` with
  `background` are refused there: nothing would ever collect the job.

### 7.4 Changelog and tests

Operator-visible, so it needs entries under `Added` (the tool, the new
`shell_exec` arguments, the config keys) and `Changed` (head-and-tail
output; the timeout behaviour per 7.2; `shell_exec` now implies
`shell_job`).

- Unit tests on a fake clock for each rule in section 4, plus: output cap
  keeps head and tail; `until` matching; `wait` on an ended job; stop of a
  job with children; limits refused with a clear message; `lost` after a
  simulated restart.
- A probe case in `tests/test_mcpserver.sh` for `shell_job` and for
  `shell_exec` with `background`.
- An extension of `tests/test_coding_harness.sh`: start a job that writes
  lines over a few seconds, read its log with the file tools, wait with
  `until`, stop it, and check the file modes.
- The capabilities golden file changes once, for `shell_job`.
- `manual/` page for the shell tools; `CLAUDE.md` "Shell guard" and
  "Shell permission" passages updated in the same change.

## 8. Left out on purpose

- **A persistent interactive shell** (one long-lived `sh` fed command
  after command). Finding where one command's output ends is fragile, and
  it hangs on anything that reads input. The remembered working directory
  (rule 6) gives most of the benefit.
- **A terminal (PTY).** It needs a third-party library, is Unix-only, and
  produces control codes that models read badly. If a real case appears (a
  program that refuses to run without a terminal), add `pty: true` to
  `shell_exec`'s background mode and strip control codes from the log.
- **Windows.** Process groups and signals differ. Build Unix first;
  elsewhere `shell_exec` keeps today's behaviour, and `background` and
  `shell_job` are refused with a clear message, not half-working.

## 9. Decisions

Settled:

- Two tools: `shell_exec` extended, and one new `shell_` tool.
- A foreground command that reaches its timeout becomes a job (7.2).
- The config keys and defaults in section 6.
- No WebUI job list (7.3).
- Built as an independent package against `toolspec`, any time after the
  refactoring starts, then swapped in and the old tool removed (section 5).

Open:

- The new tool's final name (`shell_job` is the working choice).
