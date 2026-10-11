# Conversation and memory

Each agent has one conversation, shared by every chat that reaches it, and a long-term memory that outlives that conversation.

## One conversation per agent

Every message to an agent, from any chat, device, the WebUI chat, `claw agent` or an MCP service token, goes into the same conversation, with one history and one memory. If you talk to Alice on Telegram and then in the WebUI chat, she remembers both.

Where you want a separate conversation, create a separate agent. Turn its **Cognitive memory** off if it should keep no long-term memory.

The conversation is stored in the agent's `sessions/` folder.

## How the conversation is kept short

A model can only read so much at once. ClawEh keeps the conversation within that limit in two ways, both automatic:

- **Context eviction**: large, re-readable tool results, such as a file read or a web page fetched, are replaced with a short placeholder once the agent has moved on. The agent can read the content again if it needs it.
- **Compaction**: when the conversation grows past a threshold, older messages are summarized by a memory model and only the summary and the recent messages stay in view.

Nothing is lost by compaction: the full messages and every summary stay in the agent's archive, and an agent granted the session tools (`session_search`, `session_messages`, `session_summary_list` and others) can look them up.

The thresholds are on the **System** page, in **Context management**, and apply to every agent:

| Setting | What it does |
|---|---|
| **Normal compression threshold** | Context use that triggers routine summarization (default 50%). |
| **Emergency compression threshold** | Context use that forces summarization, even mid-turn (default 80%). |
| **Minimum context threshold** | Below this, the percentage and message-count triggers never fire (default 20%). |
| **Message count threshold** | Compact after this many new messages (default 100; 0 is off). |
| **Age compaction trigger (days)** | Compact once the oldest message in view is this old (default 7). |
| **Tail age limit (days)**, **Tail token ceiling**, **Tail window size (%)**, **Minimum tail messages** | How much recent conversation is kept as it is after a compaction. |
| **Context eviction**, **Protected age**, **Expiry age**, **Read byte budget** | When large tool results are replaced by placeholders. |

The models that write the summaries are the **Memory models** on the System page (in **Agent defaults**), and the agent's own **Memory models (optional)** on its card, tried first. If none is set, the agent's own model is used.

### How long the archive is kept

Also in **Context management**:

| Setting | Default | What it does |
|---|---|---|
| **Max messages kept** | 0 (unlimited) | Keep at most this many archived messages. |
| **Message retention (days)** | 365 | Delete archived messages older than this. 0 keeps them forever. |
| **Max summaries kept** | 0 (unlimited) | Keep at most this many summaries. |
| **Summary retention (days)** | 3650 | Delete summaries older than this. 0 keeps them forever. |

## Commands in a chat

| Command | What it does |
|---|---|
| `/clear` (or `/reset`) | Starts the conversation afresh and cancels messages waiting behind it. Reply: "Context cleared, but long-term memory preserved." The archive is kept. |
| `/compact` | Summarizes the conversation now and replies with what happened. |
| `/memory` | Shows the agent's memory status: domains, pending messages, last consolidation. |
| `/cancel` | Stops the request in progress and drops the messages queued behind it. |

Because there is one conversation per agent, `/clear` from any chat clears it for every chat that talks to that agent.

## Long-term memory (cognitive memory)

With **Cognitive memory** on (the default), the agent keeps durable memories in its `cogmem/` folder and recalls the relevant ones on each turn without being asked.

- Memories are grouped into **domains** by topic. A **sticky** domain is included in every prompt; other domains are recalled when the message, or a tool just used, matches them.
- Each memory has a type: **fact**, **preference**, **rule**, **operational** or **event**. Events (things that happened at a time) are never placed in the prompt; the agent searches them when needed.
- In the background, **consolidation** reviews recent messages and writes, merges and retires memories: after 50 new messages, after 60 minutes idle, and nightly at 03:20 by default.
- The agent can manage memory itself with the `cogmem_*` tools, such as `cogmem_memory_search`, `cogmem_memory_create` and `cogmem_export`.

Memory belongs to the agent, not to a channel: what Alice learns on your phone she knows in Slack.

### Retention

Two kinds of memory are deleted by age. Set them per agent on its card:

- **Event memory retention (days)**: blank uses the default of 30 days; `-1` keeps forever.
- **Retired memory retention (days)**: how long a retired memory is kept; blank uses the default of 90 days; `-1` keeps forever.

Facts, preferences, rules and operational memories are never deleted by age.

### Review and correct memory

The **Memory** page shows each agent's memory store. There you can:

1. Change a memory's type from its row, or select many and use **Change type…**.
2. **Retire** a memory to stop it being used (it stays in the history), **Restore** it, or delete it permanently. **Show retired** reveals retired memories.
3. **Add memory** or **Add domain** by hand. What you add is marked as written by you, and the agent is told it outranks its own conclusions.
4. **Export** the store to a file, and **Import** one in **Merge** mode (add what is missing) or **Replace** mode (erase everything first).

Memory text cannot be edited: retire the memory and add a new one.

### Shape what is remembered

Write instructions in the agent's `COGMEM.md` file (in its workspace) to say what this agent should and should not record. They are added to the built-in consolidation instructions on the next run.

### Turn memory off

Turn **Cognitive memory** off on the agent's card for an agent that should work without long-term memory. It then has no `cogmem_*` tools and nothing is consolidated. The conversation itself is still kept.

## Sub-agents and memory

A [sub-agent](./subagents.md) starts with a copy of its agent's memory. Anything it writes goes to that copy, which is deleted when it finishes; the agent's own memory is never changed by it.
