# Messages between agents

Agents can ask each other questions or leave each other private notes, and a person can do the same from any chat with `/ask` and `/whisper`.

There are two kinds of message:

- An **ask** gives the other agent a normal turn with the message and waits for its reply.
- A **whisper** leaves a private note that the other agent sees at the start of its next message. It starts no turn and expects no reply.

## Who may message whom

An agent uses the `agent_message` tool. For Alice to message Bob, all of these must be true:

1. Alice has `agent_message` ticked under **Internal tools** on her card. **Default tools** does not include it.
2. Bob is in Alice's `subagents.allow_agents` in `config.json`. `"*"` allows every agent. With no list, Alice can message no one.

```json
{
  "id": "alice",
  "tools": ["agent_message"],
  "subagents": { "allow_agents": ["bob"] }
}
```

`allow_agents` has no control on the Agents page; set it in `config.json` (or **Raw config** on the System page; see [Configuration file](../reference/configuration.md)). Saving on the Agents page keeps it. The same list decides which agents Alice may [spawn](./subagents.md) and which may take part in her [forums](../forums/index.md).

Only configured agents can be messaged, never temporary ones. A message is refused when:

- Bob is not in the list: "You may not message Bob: it is not in your subagents.allow_agents."
- No agent has that name: "There is no agent named Bob."
- It is longer than 8,000 characters: "Messages to other agents are limited to 8,000 characters." Put long text in a file the other agent can read, such as one in the [common folder](./files-and-mounts.md#the-common-folder).

## Asks

The agent calls `agent_message` with `wait_seconds` above 0.

- Bob gets a normal turn in his one conversation, behind anything he is already doing, with **his own** tools. The message starts with `[Message from Alice — Alice is waiting for your reply]`.
- Alice waits at most `wait_seconds`, or Bob's model **Request Timeout**, whichever is shorter. If Bob has not answered by then, Alice is told "Bob did not reply within N seconds." Bob's turn carries on, but its late reply is dropped.
- Bob's reply goes only to Alice, as the result of her tool call. Nothing from his turn is posted to any chat.
- Other outcomes Alice may see: "Bob's turn failed: …", "Bob's turn was cancelled before it replied." and "Bob gave no reply."
- Restarting ClawEh abandons asks in progress.

### Limits on asks

- Every ask counts as one level of the sub-agent depth limit (**Max sub-agent depth** on the System page, default 3), shared with `agent_spawn` and Maestro. An ask from a turn already at the limit is refused.
- An agent that is waiting cannot be asked by the agent it waits for: if Alice asks Bob, Bob cannot ask Alice back, directly or through other agents.
- While Alice waits she gives up her place among the turns allowed to run at once, so asks cannot block each other.

## Whispers

The agent calls `agent_message` with `wait_seconds` 0. Alice is told "Whispered to Bob." at once.

The note is added to the start of the next message Bob receives, whatever its source:

```
[Private whisper from Alice — no reply expected. To answer privately, use agent_message with wait_seconds 0.] <text>
```

The second sentence appears only when Bob could do it: he has `agent_message` and Alice is in his `allow_agents`. Each whisper is delivered once. Whispers not yet delivered are lost when ClawEh restarts.

## /ask and /whisper from a chat

A person can type, in any chat:

```
/ask Bob what is on today's list?
/whisper Bob the report is in common/report.md
```

The agent can be named by id or name. The person must already be allowed to talk to Bob from that chat: the chat [routes](../channels/routing.md) to Bob, or a binding that matches this channel and chat names Bob as its agent or in its `agent_mentions`.

| Case | The person sees |
|---|---|
| `/ask` allowed | Bob's reply posted in the chat as "Bob: \<reply\>". The chat is not held while Bob works. |
| `/whisper` allowed | "Whispered to Bob." |
| Not allowed | "You don't have permission to /ask Bob." (or /whisper) |
| Unknown agent | "There is no agent named Bob." |
| Something failed | "Could not ask Bob." or "Could not whisper to Bob.", with the reason when it is the depth limit or a loop |

Bob is told the sender is a person and how they wrote, so a person cannot pass for an agent of the same name: `[Message from Alice (a person, via /ask on telegram) — Alice is waiting for your reply]`. From the WebUI chat the sender is "the WebUI user".

## Asking a person

Bob may be a [human agent](./human-agents.md). The ask is then posted to his chat and his next text there is the reply.
