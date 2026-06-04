# Telegram Integration Setup

ClawEh connects to Telegram using a bot token obtained from **@BotFather**. The
bot receives messages over long polling — no public webhook URL is required.

---

## 1. Create a bot and get a token

1. In Telegram, open a chat with [@BotFather](https://t.me/BotFather).
2. Send `/newbot` and follow the prompts to choose a name and username.
3. BotFather replies with a **bot token** (e.g. `123456:ABC-DEF...`). Keep it secret.

---

## 2. Configure the bot

Add a Telegram bot entry under `channels.telegram` in your config. Each entry
creates a channel named `telegram-<id>` (an empty or `"default"` id creates the
plain `telegram` channel).

```json
{
  "channels": {
    "telegram": [
      {
        "id": "default",
        "enabled": true,
        "token": "123456:ABC-DEF...",
        "allow_from": ["123456789"]
      }
    ]
  }
}
```

Always set `allow_from` explicitly — on Telegram bots are publicly discoverable
by username, so an open bot can be reached by anyone.

---

## 3. Using the bot in group chats

> **Important:** A newly created Telegram bot has **Group Privacy enabled** by
> default. While Group Privacy is on, Telegram only delivers the following group
> messages to the bot:
>
> - messages that start with a `/command`
> - messages that **@mention** the bot
> - replies to one of the bot's own messages
>
> Ordinary group messages are **never delivered** to the bot — they will not
> appear in the logs at all, because Telegram filters them server-side before
> they reach ClawEh. The usual symptom is a bot that responds once (to a command
> or mention) and then appears to ignore everything else.

To let the bot see and respond to all messages in a group or channel, **turn off
Group Privacy**:

1. Open [@BotFather](https://t.me/BotFather) and send `/mybots`.
2. Select your bot → **Bot Settings** → **Group Privacy** → **Turn off**.
   (Equivalently: send `/setprivacy`, pick the bot, and choose **Disable**.)

The change takes effect immediately for bots that are already members of a
group — **you do not need to remove and re-add the bot**.

### Restricting when the bot responds

If you would rather leave Group Privacy on (or keep the bot quiet in busy
groups), use `group_trigger` to control when ClawEh responds. With no
`group_trigger` configured, the bot responds to every message it receives.

```json
{
  "id": "default",
  "enabled": true,
  "token": "123456:ABC-DEF...",
  "group_trigger": {
    "mention_only": true,
    "prefixes": ["!claw "]
  }
}
```

- `mention_only` — respond only when the bot is @mentioned or replied to.
- `prefixes` — respond when a message starts with one of these prefixes (the
  prefix is stripped before the message reaches the agent).

Note that `group_trigger` only governs messages Telegram actually delivers. If
Group Privacy is on, the bot can never see plain chatter regardless of these
settings.

---

## 4. Combining split messages (coalescing)

When a user types or pastes a long message, the Telegram app splits it into
several messages (Telegram caps a single message at 4096 characters, and long
pastes are sent as separate messages when you hit send). Without coalescing,
each fragment is processed as its own turn: the agent answers a partial
instruction, and the next fragment is then seen as a follow-up to that reply.

Coalescing buffers consecutive messages from the **same sender in the same
chat** and combines them into one inbound message once no new message has
arrived for a short quiet period. Fragments are reassembled in send order
(by Telegram message ID) and joined with newlines.

It is **enabled by default** with a 1000 ms window. Configure it with the
`coalesce` block:

```json
{
  "id": "default",
  "enabled": true,
  "token": "123456:ABC-DEF...",
  "coalesce": {
    "enabled": true,
    "window_ms": 1000,
    "max_messages": 50,
    "max_wait_ms": 30000
  }
}
```

- `enabled` — turn coalescing on or off. Set to `false` to process every message
  immediately (the previous behavior).
- `window_ms` — quiet period to wait after the most recent message before
  flushing. Each new message resets the timer. Defaults to `1000`. Telegram
  delivers split messages almost instantly, so even `500` is usually enough;
  `1000` is safer.
- `max_messages` — flush regardless of the timer once this many messages have
  buffered. Defaults to `50`.
- `max_wait_ms` — flush regardless of the timer once this much time has elapsed
  since the first buffered message, so a sender who keeps typing cannot hold the
  buffer open indefinitely. Defaults to `30000`.

Bot commands (e.g. `/cancel`, `/status`) bypass the buffer — they are never
delayed or merged, and they flush any pending buffered text first. In
mention-only groups, coalescing only applies to the messages Telegram actually
delivers to the bot (those that mention it), so a split paste that mentions the
bot only in its first fragment is not fully combined.
