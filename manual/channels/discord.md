# Discord

Connect a Discord bot so people can talk to your agents in direct messages and on Discord servers.

## Create the bot

1. Open the [Discord Developer Portal](https://discord.com/developers/applications)
   and create a new application.
2. On the application's **Bot** page, create the bot and copy its token.
   Keep it secret.
3. Invite the bot to your server with an invite link from the portal that
   includes the `bot` scope and permission to view channels, read message
   history and send messages.

## Connect ClawEh

1. In the WebUI sidebar, open **Channels → Discord**.
2. Turn on **Enable channel**.
3. Paste the token into **Bot Token**.
4. In **Allow From**, enter the Discord user IDs of the people who may write,
   separated by commas. (In Discord, turn on Developer Mode, then right-click a
   user and copy the user ID.) An empty list lets nobody in; `*` lets anyone
   in.
5. Optional: turn on **Mention Only** so the bot answers on servers only when
   it is mentioned.
6. Optional: set **HTTP Proxy** if ClawEh reaches Discord through a proxy.

Settings save as you type.

## Direct messages and servers

- **Direct messages** to the bot are always answered.
- **On a server**, ClawEh does not request Discord's privileged *Message
  Content* access, so Discord passes it the text of a server message only when
  the message mentions the bot. In practice, mention the bot (`@Alice …`) to
  talk to it in a server channel.

To answer only messages that start with a keyword, set `group_trigger` in
`config.json`:

```json
{
  "channels": {
    "discord": {
      "group_trigger": { "prefixes": ["!alice "] }
    }
  }
}
```

## Which agent answers

The channel name is `discord`. Bind it to an agent on the **Bindings** page.
In `config.json`, a binding can also name one Discord channel (peer kind
`channel` and the channel ID), one person's direct messages (peer kind
`direct` and the user ID), or a whole server (`guild_id`). See
[Routing and bindings](./routing.md).

## When something goes wrong

- A message to an unknown channel or user, or to a user who does not accept
  messages from the bot, is recorded as **can't be reached** and logged as a
  warning; there is no alert and no retry.
- **Discord down** (alert): no working connection to Discord for ten minutes.
  ClawEh keeps reconnecting and raises **Discord up** when it is back.
- Long replies are split into several messages, because Discord limits a
  message to 2,000 characters.

## Related pages

- [Routing and bindings](./routing.md)
- [Channels overview](./index.md)
