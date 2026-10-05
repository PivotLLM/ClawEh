# Agent-to-agent messages

One agent can send a message to another, and a person can do the same from
any chat. There are two kinds of message:

- An **ask** gives the other agent a normal turn with the message and waits
  for its reply.
- A **whisper** leaves a private note that the other agent sees at the start
  of its next message. It never starts a turn and expects no reply.

Agents reach them with the `agent_message` tool; people with the `/ask` and
`/whisper` commands. The examples use two agents, Alice and Bob.

## The `agent_message` tool

| Parameter | Meaning |
|---|---|
| `agent` | The agent to message, by id or name. |
| `message` | The message. |
| `wait_seconds` | `0` whispers and returns at once ("Whispered to Bob."). More than `0` asks and returns Bob's reply, or "Bob did not reply within N seconds." |

Who may use it:

- The tool is off by default. Switch it on like any other tool (the per-tool
  switch, `tools.tool_overrides.agent_message: true`), and the agent's
  `tools` list must allow it. It belongs to the `agent` tools, so it also
  needs `tools.subagent.enabled`, like `agent_spawn`.
- Alice can message Bob only when Bob is in Alice's
  `subagents.allow_agents` (`"*"` allows every agent). With the default empty
  list Alice can message no one. Only configured agents can be messaged, never
  temporary ones.
- `deny_tools` removes it as it removes any tool.
- A sub-agent clone of Alice uses Alice's list and speaks as Alice. A fresh
  temporary agent has no tools.

The reply comes back as the tool result, so Alice does not treat it as a
conversation to continue. Nothing is ever sent back automatically.

## Asks

When Alice asks Bob:

- Bob gets a normal turn in his one conversation (`agent:bob:main`), queued
  behind whatever Bob is already doing, with his full agent loop and his own
  tools. The message starts with
  `[Message from Alice — Alice is waiting for your reply]`.
- Alice waits at most `wait_seconds`, or Bob's model `request_timeout`
  (the `agents.defaults.request_timeout` when the model sets none), whichever
  is shorter. The tool call's own time limit (`agents.defaults.tool_timeout`)
  also applies. If Bob has not answered by then, Alice gets "Bob did not
  reply within N seconds."; Bob's turn carries on, and its late reply is
  dropped (logged).
- Bob's reply goes only to Alice. It is never posted to a chat, and anything
  else Bob's turn would show in its own chat (progress notes, tool output) is
  dropped, for agents on CLI models too. In the asked turn `msg_send` answers
  "msg_send needs a target in an asked turn", and `session_clear` is refused.
- Background work Bob starts in the asked turn (an `agent_spawn` in callback
  mode, say) reports to Bob's own main conversation once it finishes, and
  stays there: nothing is sent to any chat.
- Turn slots (`agents.defaults.max_concurrent_turns`): Alice lends hers while
  she waits, so Bob can finish a message queued ahead of the ask, and an asked
  turn never waits for a slot itself. Asks are bounded by the depth limit
  below instead.
- Bob may be a person (a human agent, see `human-agents.md`): the ask is
  posted to his chat and his next text there is the reply.
- If Alice stopped waiting (timeout, `/cancel`, shutdown) before Bob got to
  the ask, Bob does not answer it at all.
- A failed turn returns "Bob's turn failed: …", a turn stopped with `/cancel`
  "Bob's turn was cancelled before it replied.", and an empty one "Bob gave no
  reply.".
- `shell_exec` in Bob's turn follows where the exchange began: if it began
  with a message on a chat (Telegram, Slack, …), it answers "shell_exec is
  off for work started from a chat; set tools.exec.allow_remote to allow it."
  unless `tools.exec.allow_remote` is on, as it would in that chat. An
  exchange that began locally (the CLI, the forum) keeps today's behaviour.
- Restarting ClawEh abandons asks in progress; an asked turn is not replayed.

### Depth

Every ask counts as one level of sub-agent depth, shared with `agent_spawn`
and Maestro dispatch (`agents.defaults.max_subagent_depth`, default 3). Bob's
turn runs one level deeper than Alice's, and an ask from a turn already at
the limit is refused, so a chain of asks is bounded.

An agent that is itself waiting cannot be asked: if Alice asks Bob, Bob
cannot ask Alice back (or himself), whether from the asked turn or from
another of his turns, because each would wait for the other until the
timeout. The refusal also covers longer cycles through other agents back to
the asker, and agents on CLI models: their tool calls
reach ClawEh over MCP with the same check.

## Whispers

When Alice whispers to Bob, the text is held and added to the start of the
next message Bob receives, whatever its source (a person, an ask, a scheduled
job, a sub-agent result), as:

```
[Private whisper from Alice — no reply expected. To answer privately, use agent_message with wait_seconds 0.] <text>
```

The last sentence is shown only when Bob has the `agent_message` tool. Each
whisper is delivered once. Whispers are kept in memory: any not yet delivered
are lost when ClawEh restarts.

## `/ask` and `/whisper`

From any chat:

```
/ask Bob what is on today's list?
/whisper Bob the report is in common/report.md
```

The agent can be named by id or name. The person must already be allowed to
chat with that agent from where they are: the chat must route to it, or a
binding that matches this channel, account and chat (peer, guild or team)
names it as its agent or among its `agent_mentions` (`"*"` names every agent).
The channel's own allow list applies first, as for every message.

- Allowed `/whisper`: "Whispered to Bob."
- Allowed `/ask`: Bob's reply is posted to the chat as "Bob: <reply>". The ask
  runs in the background, so the chat is not held while Bob works; it waits up
  to Bob's `request_timeout` (the turn timeout when none is set).
- Not allowed: "You don't have permission to /ask Bob" (or `/whisper`).
- Unknown agent: "There is no agent named Bob."

Bob sees the person marked as a person, with the command and the channel, so
they cannot be taken for an agent of the same name:
`[Message from Alice (a person, via /ask on telegram) — Alice is waiting for your reply]`
and `[Private whisper from Alice (a person, via /whisper on telegram) — …]`.
A `/ask` still waiting when ClawEh stops is abandoned without a reply.

## For code: the `Messenger` interface

The tool and the commands are thin wrappers over two core functions on the
agent loop, `tools.Messenger`:

```go
Ask(ctx, from, agentID, message string, wait time.Duration) (tools.AgentReply, error)
Whisper(ctx, from, agentID, message string) error
```

`AgentReply{Text, Outcome}` carries the final reply and one of `ok`,
`error`, `cancelled`, `empty` (the bus turn outcomes) or `timeout`
(`tools.OutcomeTimeout`). These do no `allow_agents` or channel check; the
caller decides who may message whom. `Ask` accepts any agent in the registry,
temporary agents included, and returns `tools.ErrNoSuchAgent`,
`tools.ErrMaxDepth` or `tools.ErrAskLoop` when it refuses.
