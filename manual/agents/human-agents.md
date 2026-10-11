# Human agents

A human agent lets a person take part as an agent: other agents ask it questions, and the person answers by hand in their own chat.

No model runs for a human agent. When an agent asks Bob something, the question is posted to Bob's chat and Bob's next text message there is the answer.

## Set one up

The example sets up Bob, who answers in a Telegram direct chat.

1. **Providers** page ([Providers and models](../providers/concepts.md)): add a provider with protocol `human`. It needs no address or key.
2. **Models** page: add a model on that provider.
   - Give it a name of its own that names the person, for example `Bob (human)`. No other model may use that name.
   - The model id can be any placeholder, such as `bob`.
   - Set **Request Timeout (s)** to how long Bob may take to answer. 0 uses the install's default request timeout.
3. **Agents** page: add an agent, for example `bob`, whose **Models (tried in order)** is only that model. Its card then shows only the models, the line "Represents a person: answers are typed by hand." and **Default Channel**.
4. **Bindings** page ([Routing and bindings](../channels/routing.md#bind-one-chat-to-an-agent)): bind one chat used by Bob alone (a Telegram direct chat, a Slack channel of his own) to the agent. It must name one chat, not a whole bot, and no other agent may be bound to it.
5. Back on the agent's card, pick that binding as its **Default Channel**.

The agent is not run until it has its default chat. In `config.json` the result looks like this:

```json
{
  "providers": [{ "name": "People", "protocol": "human" }],
  "models": [{ "model_name": "Bob (human)", "model": "bob", "provider": "People",
               "enabled": true, "request_timeout": 3600 }],
  "agents": { "list": [{ "id": "bob", "name": "Bob", "models": ["Bob (human)"] }] },
  "bindings": [{ "agent_id": "bob", "default": true,
                 "match": { "channel": "telegram-main",
                            "peer": { "kind": "direct", "id": "4242" } } }]
}
```

### Rules

- A human agent has exactly one binding, its default, naming one chat that no other agent is bound to.
- It is never the default agent.
- A human model is never another agent's model, fallback, memory, vision, image or sub-agent model.

A WebUI save that would break a rule is refused with a one-line reason. If `config.json` is edited by hand into breaking one, the agent is set aside: its card says "Not running: …" with the reason, and messages from its chat get "Bob is not running." A human model named where a model must answer is ignored there, with a note on the agent's card or on the System or Models page. **Check Up** marks a human agent whose chat is on a channel that is not set up.

## Asking Bob

Agents ask Bob the same way they ask any agent: with the `agent_message` tool, the `/ask` command or a [forum](../forums/index.md#a-person-in-a-forum) turn (see [Messages between agents](./messaging.md)). The asking agent must be allowed to message Bob.

## What Bob sees

- The question arrives in his chat as it was asked, with its sender line, for example `[Message from Alice — Alice is waiting for your reply]`. He never sees instructions or earlier history.
- A whisper to Bob is shown once, at the start of the next question posted to him.
- His next text message is the answer. Nothing is sent back to confirm it.
- An attachment alone is not an answer: "Please answer with text."
- Text when nothing is waiting: "Nothing is waiting for your answer."
- An answer after the time ran out: "That request has already timed out."
- If the asker gives up first, the question is withdrawn with "Alice no longer needs an answer to that request.", and a later answer gets "That request was withdrawn."
- `/cancel` ends the waiting question. Other commands work as usual.
- Questions come one at a time. The next is posted once the current one is answered, cancelled or timed out.

## What the asker sees

| What happened | The asking agent is told |
|---|---|
| Bob answered | Bob's text |
| No answer within Bob's request timeout, or before the asker stopped waiting | "Bob did not reply within N seconds." |
| Bob used `/cancel` | "Bob cancelled the request." |
| No channel of that name is configured | "Bob's chat is not set up." |
| The channel is not running | "Bob's chat is unavailable." |
| A paired device that is not connected | "Bob's device is offline." |
| An unknown chat, a blocked bot, an archived conversation, an unpaired device | "Bob's chat can't be reached." |
| The send failed after its retries, or anything else | "Couldn't reach Bob's chat." |

When the question cannot be posted, the asker is told at once and Bob is not waited for.

## What a human agent never does

- It answers only questions from agents. Anyone writing to it directly is told "Bob only answers questions from agents."
- Scheduled jobs and external messages cannot be sent to it; scheduling a job for it is refused.
- It has no tools and no long-term memory, and no model ever reads its conversation. Images and voice messages to it are not described or transcribed.
- It is never cloned or spawned as a [sub-agent](./subagents.md).
- Devices do not list it among the agents they can pick.
- A question waiting when ClawEh shuts down is cancelled, not posted again after the restart.
