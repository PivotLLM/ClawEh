# WebUI chat

The **Chat** page in the WebUI lets you talk to an agent from your browser, with no other app to set up.

## Use the chat

1. Sign in to the WebUI (see [Signing in](../admin/login.md)).
2. Click **Chat** at the top of the sidebar.
3. Type in the message box and press Enter.

The chat needs at least one model. If none is set up, the page says **No
Model Configured** and offers **Go to Models** and **Run setup wizard**.

## Which agent answers

The WebUI chat is the channel `webui`. It follows the same rules as every
other channel (see [Routing and bindings](./routing.md)):

- With a binding for the channel `webui`, that binding's agent answers.
- Without one, the [default agent](../agents/creating.md#the-default-agent-and-default-models) answers.

The chat has no agent picker. To talk to another agent from the chat, either
add a binding, or use `/ask`:

```
/ask Bob What is on my calendar today?
```

Bob's reply appears in the chat as "Bob: …". `/ask` works only when this chat
may already reach Bob: the chat routes to him, or a matching binding names him
as its agent or in its agent mentions. Otherwise the answer is "You don't have
permission to /ask Bob." See [Messages between agents](../agents/messaging.md#ask-and-whisper-from-a-chat).

## New Chat does not start a new conversation

**New Chat** clears the chat window. It does not reset the agent: each agent
has one conversation shared by all channels (see
[Conversation and memory](../agents/memory.md)), so the agent still remembers what
was said before, here or on any other channel.

## Setup

ClawEh turns the WebUI chat on by itself at startup, with a channel token and
**Allow From** set to `*`, so there is nothing to configure. The settings are
under **Channels → Web**.

Your browser uses your WebUI login. The channel token on that page is only for
other programs that connect to the chat socket directly.

## Things to know

- The WebUI chat cannot be an agent's default channel (where scheduled output
  goes), because it has no fixed address. The **Default Channel** section on
  the **Agents** page marks it "can't be a default channel".
- A message sent to the WebUI chat while no browser has it open is not
  delivered; ClawEh records the chat as offline. See
  [When a message cannot be delivered](./index.md#when-a-message-cannot-be-delivered).
