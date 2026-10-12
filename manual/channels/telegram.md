# Telegram

Connect one or more Telegram bots so people can talk to your agents in Telegram.

ClawEh fetches messages from Telegram itself, so you do not need a public
address or a webhook.

## Create a bot with BotFather

1. In Telegram, open a chat with [@BotFather](https://t.me/BotFather).
2. Send `/newbot` and follow the prompts to choose a display name and a
   username for the bot.
3. BotFather replies with the **bot token** (it looks like
   `123456:ABC-DEF...`). Keep it secret: anyone with the token controls the bot.

## Add the bot to ClawEh

1. In the WebUI sidebar, open **Channels → Telegram**.
2. Click **Add Bot**.
3. In the id field, enter a short id for the bot, such as `alice`, or leave it
   blank for your first bot.
4. Paste the token into **Bot Token**.
5. In **Allow From**, enter the Telegram users who may write to the bot,
   separated by commas (see [Who may write to the bot](#who-may-write-to-the-bot)).
6. Leave **Enable channel** on and click **Save**.
7. Open [Bindings](./routing.md) and bind the bot's channel to the agent that
   should answer. Without a binding, the
   [default agent](../agents/creating.md#the-default-agent-and-default-models) answers.
8. Test it: in Telegram, search for the bot's username, open the chat and
   press **Start** (or send `/start`). Then send a message; the agent should
   answer. A bot cannot write to anyone who has not started a chat with it
   first, so each person who should receive messages from the agent, such as
   scheduled ones, must do this once.

The bot's channel name, which you use in bindings, is `telegram` for a bot with
a blank id (or the id `default`) and `telegram-<id>` for any other, for example
`telegram-alice`.

To change a bot later, click **Edit** on its card. Changes save as you type;
click **Done** to close the card. The switch on the card turns the bot on or
off, and the trash button removes it.

### Other settings on the card

- **API Base URL** and **HTTP Proxy**: leave blank unless you reach Telegram
  through a proxy or a different API server.
- **Typing Indicator**: shows "typing…" while the agent works.
- **Placeholder Message**: posts a short message (for example "Thinking...")
  that is replaced by the reply.
- **Combine Split Messages**: Telegram splits a long paste into several
  messages. With this on (the default), ClawEh waits briefly
  (**Combine Window (ms)**, 1000 by default) and hands them to the agent as
  one message. Commands such as `/cancel` are never delayed.

## Several bots

Each bot is its own channel. Add one bot per agent when people should reach
each agent by a separate Telegram contact:

1. Create a second bot with BotFather.
2. Click **Add Bot** again and give it a different id, for example `bob`.
3. Bind `telegram-bob` to Bob on the **Bindings** page.

Each bot needs its own token. Two bots must not share one token.

## Who may write to the bot

**Allow From** lists who may use the bot. Bots can be found by anyone who
knows the username, so always fill it in. An empty list lets nobody in.

Accepted entries:

- a numeric Telegram user ID, such as `123456789`;
- a username with `@`, such as `@alice_example`;
- `*` to let anyone in (not recommended).

A username is the easiest to enter: it is shown on the person's Telegram
profile. A numeric ID keeps working if the person changes their username.

Messages from anyone else are ignored without a reply.

**Allow From** is checked against the person who sends the message, also in
groups: in a group, only listed people get an answer.

## Groups

1. Add the bot to the group in Telegram.
2. Decide whether the bot should see all messages or only those addressed to
   it.

A new Telegram bot has **Group Privacy** turned on. Telegram then delivers only
commands, messages that @mention the bot, and replies to the bot's own
messages; ordinary group messages never reach ClawEh and do not appear in its
log. To let the bot see every message:

1. In [@BotFather](https://t.me/BotFather), send `/mybots`.
2. Choose the bot, then **Bot Settings → Group Privacy → Turn off**.

This takes effect at once; you do not need to re-add the bot.

To keep the bot quiet in a busy group, set `group_trigger` for the bot in
`config.json` (it is not on the WebUI card):

```json
{
  "id": "alice",
  "token": "123456:ABC-DEF...",
  "group_trigger": {
    "mention_only": true,
    "prefixes": ["!alice "]
  }
}
```

- `mention_only`: answer only when the bot is mentioned or replied to.
- `prefixes`: also answer messages that start with one of these texts; the
  prefix is removed before the agent sees the message.

Without `group_trigger`, the bot answers every group message it receives.
Private chats with the bot are always answered.

To send a group to a different agent than the bot's private chats, bind the
group's chat ID (see [Routing and bindings](./routing.md#bind-one-chat-to-an-agent)).

## When the bot does not answer

- **The sender is not in Allow From**: the message is ignored without a reply
  and without a warning in the log. Add the person's username or ID.
- **The bot is switched off**: check the switch on its card.
- **A group message is not answered**: see [Groups](#groups); with Group
  Privacy on, Telegram never delivers ordinary group messages.
- **Another agent answers**: check the bot's binding on the **Bindings** page.

## When the bot is blocked or removed

If a person blocks the bot, deletes their account, or never started a chat
with it, or the bot is removed from a group, Telegram refuses the message.
ClawEh records that the chat **can't be reached**, logs a warning and moves
on. It does not retry and does not raise an alert. The same happens when a
group is upgraded to a supergroup: Telegram gives it a new chat ID, so update
any binding or default chat that used the old one.

An agent that tried to send the message is told so, for example "Not sent:
… can't be reached."

What does raise an [alert](../admin/alerts.md):

- **Telegram polling failed**: Telegram rejected the bot token (it was revoked
  or is wrong). Create a new token with BotFather (`/revoke`) and paste it into
  **Bot Token**.
- **Telegram down**: the bot has had no working connection to Telegram for ten
  minutes. ClawEh keeps retrying and raises **Telegram up** when the
  connection is back.

Bot tokens never appear in ClawEh's logs or alerts.

## Commands

The bot registers its commands in Telegram's command menu, including `/ask`
and `/whisper` for [messaging other agents](../agents/messaging.md). Type `/` in the chat to see them.

## Related pages

- [Routing and bindings](./routing.md)
- [Channels overview](./index.md)
