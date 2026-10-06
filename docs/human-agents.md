# Human agents

A human agent lets a person take part as an agent. Agents ask it questions
(the `agent_message` tool, `/ask`, a forum turn) and the person answers by
hand. ClawEh runs no model for it: the question is posted to the person's chat
and their next message there is the answer.

## Set one up

1. **Providers page:** add a provider with protocol `human`. It has no base URL
   or API key.
2. **Models page:** add a model on that provider. The model name names the
   person, for example `Bob (human)`, and no other model may use that name;
   the model id can be any placeholder (`bob`). Set `request_timeout` to how
   long the person may take to answer, in seconds (0 uses
   `agents.defaults.request_timeout`).
3. **Agents page:** add an agent (for example `bob`) whose model list is only
   that model. It must not be the default agent.
4. **Channels:** bind one chat used by that person alone (a Telegram direct
   chat, a Slack channel of their own) to the agent, and make it the agent's
   default. That is the agent's only binding, it names one chat (not a whole
   bot), and no other agent may be bound to that chat.

```json
{
  "providers": [{"name": "People", "protocol": "human"}],
  "models": [{"model_name": "Bob (human)", "model": "bob", "provider": "People",
              "enabled": true, "request_timeout": 3600}],
  "agents": {"list": [{"id": "bob", "name": "Bob", "models": ["Bob (human)"]}]},
  "bindings": [{"agent_id": "bob", "default": true,
                "match": {"channel": "telegram-main",
                          "peer": {"kind": "direct", "id": "4242"}}}]
}
```

## What the person sees

- A question arrives in their chat as it was asked, with its sender header
  (`[Message from Alice — Alice is waiting for your reply]`). They never see
  a system prompt or earlier history.
- A whisper to the human agent (`agent_message` with `wait_seconds` 0,
  `/whisper`) is shown at the start of the next question posted to the
  person, once.
- Their next text message in that chat is the answer; the "thinking"
  indicator for it is cleared and nothing is sent back. An attachment alone
  is not an answer: they are asked for text.
- If no answer arrives within the model's `request_timeout` (counted from when
  the question is posted), or before the asker stops waiting if that is
  sooner, the asker is told "Bob did not reply within N seconds.". A late
  answer is told "That request has already timed out."
- If the question cannot be posted to the chat, the asker is told at once
  why, and the person is not waited for:

  | The channel reports | The asker is told |
  |---|---|
  | No channel of that name is configured | "Bob's chat is not set up." |
  | The channel is not running | "Bob's chat is unavailable." |
  | The recipient is offline (a paired device that is not connected) | "Bob's device is offline." |
  | The recipient doesn't exist (an unknown chat, a user who blocked the bot, an unpaired device) | "Bob's chat doesn't exist." |
  | The send failed after its retries, or anything else | "Couldn't reach Bob's chat." |

  Of these, only a channel that is not running or a send that failed after
  its retries raises the "Channel send failed" alert. Check Up marks a human
  agent whose chat is on a channel that is not set up.
- If the asker gives up first (its turn is cancelled, or claw shuts down),
  the question is withdrawn with "Alice no longer needs an answer to that
  request.", and a later answer is told "That request was withdrawn." An
  answer that arrives just as the asker gives up gets the same withdrawal
  line. No answer is dropped without a word.
- Questions to one person are answered one at a time; the next is posted once
  the current one is answered, cancelled or has timed out. While the person is
  thinking, nothing counts against `agents.defaults.max_concurrent_turns`:
  the asking agent lends its slot while it waits, and the question holds none.
- `/cancel` ends the waiting question; the asker is told "Bob cancelled the
  request.".
  Any other command (only `/` starts one in this chat) is handled as usual,
  even while a question waits.
- Text that answers nothing gets "Nothing is waiting for your answer." and
  starts no turn.
- If claw shuts down while a question waits, the question is cancelled, not
  posted again after the restart.

## What a human agent never does

- It takes work only from questions asked by agents. A scheduled job, a
  webhook or Integration Token message, a system notice or a restart replay is
  never posted to the person: scheduling a job for a human agent and sending
  it an external message are refused, and anything else is dropped with a log
  line. Someone who writes to it directly (an `@bob` mention, a chat routed
  to it) is told "Bob only answers questions from agents." Devices (the
  `/agent` list, `agents.list`) do not offer human agents.
- No model ever sees its conversation: it has no tools, no cognitive memory,
  no summarization or compaction, and its images and voice messages are not
  described or transcribed. The exchange is not kept in its session history.
- It is never cloned or spawned: `agent_spawn` and Maestro dispatch targeting
  it, and temporary agents on a human model, are refused.
- It is never the default agent, and a human model is never another agent's
  model, fallback, summarization, vision, image or sub-agent model.

## When the configuration breaks the rules

A WebUI save is refused with a one-line reason when it would break a rule. A
human agent without a default chat can be saved (the binding can only be added
once the agent exists) but is not run until it has one.

Anything that breaks a rule anyway (for example a hand edit of `config.json`)
is set aside at startup and on every reload, and logged:

- the agent is not run, and its card on the Agents page says "Not running" and
  why. Messages from its chat get "Bob is not running." and never reach
  another agent;
- a human model named where a model must answer is ignored there, with a note
  on the agent's card or on the System or Models page.

`GET /api/agents/human` lists the human agents and these problems.
