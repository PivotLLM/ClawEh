# SecMsg (Signal)

Connect Signal, or another secure messenger, so people can talk to your agents there.

ClawEh does not talk to Signal directly. It connects to a separate
**secure-messaging daemon**, a program that holds the messenger account and
speaks the SecMsg protocol, such as `sigd` for Signal. The daemon is not part
of ClawEh; install and run it by its own instructions first.

## What you need from the daemon

- **Its address**: the host and port it listens on, for example
  `127.0.0.1:9801`. ClawEh connects over plain TCP, without encryption or a
  password, so run the daemon on the same machine as ClawEh and keep it
  listening on `127.0.0.1`.
- **An account**: a messenger account linked to the daemon. One daemon can
  hold several accounts (several phone numbers). If it has none yet, you link
  one from the WebUI (see [Link an account](#link-an-account)).
- **Read access to its files**: the daemon saves attachments people send on
  disk and ClawEh reads them from there, so ClawEh's user must be able to read
  the daemon's attachment folder.

ClawEh learns which service the daemon speaks (for example `signal`) when it
connects.

## Channel names

Each account on a daemon becomes its own channel, named
`<daemon name>-<account>`, for example `signal-alice` for the account `alice`
on the daemon named `signal`. Each account can be bound to a different agent.
A daemon without a name is called `secmsg`.

## Connect ClawEh

1. In the WebUI sidebar, open **Channels → SecMsg** and click **Add Daemon**.
2. In the name field, enter a short name, for example `signal`. It starts
   every channel name of this daemon.
3. Leave **Enable channel** on.
4. Fill in **Daemon Address**, for example `127.0.0.1:9801`.
5. In **Allow From**, enter who may write to any account on this daemon,
   separated by commas (see [Who may write](#who-may-write)).
6. Click **Save**.
7. Click **Edit** on the daemon's card. Under **Accounts**, ClawEh lists the
   accounts the daemon already has, each with the channel name it uses, for
   example `alice → signal-alice`. The card's title shows how many accounts
   the daemon has, or "daemon unreachable".
8. Open [Bindings](./routing.md) and bind each account's channel to the agent
   that should answer. Without a binding, the
   [default agent](../agents/creating.md#the-default-agent-and-default-models)
   answers.

After the first save, changes save as you type; click **Done** to close the
card. The switch on the card turns the daemon on or off, and the trash button
removes it.

ClawEh asks the daemon for its accounts when it starts and whenever the
configuration is saved. An account linked later is picked up at the next save
or restart.

## Link an account

To link the daemon to the messenger app on your phone:

1. If the daemon already lists the account under **Accounts**, click **Link
   device** under it. Otherwise:
   1. Click **Show advanced: pin specific accounts**, then **Pin account**.
   2. In **Account**, enter a short name for the new account, for example
      `alice`. The channel will be `signal-alice`.
   3. Wait a moment for the change to save, then click **Link device** in the
      account's **Device Linking** section.
2. A QR code appears. On your phone, open the messenger's linked-devices
   screen (in Signal: **Settings → Linked devices → Link new device**) and
   scan it.
3. The page shows **Linked** when it is done. The phone lists the new device
   as "ClawEh".

If linking fails or the code is no longer accepted, click **Link device** (or
**Regenerate QR**) again and scan the new code.

## Pinned accounts

Without pinned accounts, ClawEh binds every account the daemon has, each with
the daemon's **Allow From** and **Mention Only**. Pin an account to give it
its own settings:

- **Account**: the account on the daemon. Left blank, the daemon's only
  account is used; with several accounts, ClawEh does not connect until you
  fill it in.
- **Channel Name**: a different channel name, instead of
  `<daemon name>-<account>`.
- **Allow From**: who may write to this account. Left empty, the daemon's
  list applies.
- **Mention Only**: see [Groups](#groups).

Once you pin any account, only the pinned accounts become channels; the
daemon's other accounts are not connected. Remove the pins (the trash button
on each) to go back to all accounts.

## Who may write

**Allow From** lists who may use the accounts. An empty list lets nobody in;
`*` lets in anyone who can message the account (not recommended).

Enter each person by the ID the daemon reports for them. For Signal this is
usually a long account ID rather than a phone number, so the easiest way to
find it is:

1. Have the person send the account a message. It is ignored.
2. Open the **Logs** page and find the warning "Message from unauthorized
   sender — add to allow_from to permit".
3. Copy the `allow_from` value from that line, for example
   `signal:1a2b3c4d-…`, into **Allow From**.

Both forms work: with the service in front (`signal:1a2b3c4d-…`) or the bare
ID (`1a2b3c4d-…`).

**Allow From** is checked against the person who sends the message, also in
groups: in a group, only listed people get an answer.

## Groups

The protocol does not tell ClawEh whether the account was mentioned, so group
messages are handled by these rules:

- By default, the account answers every group message from a listed sender.
- With **Mention Only** on, the account never answers in groups. Direct
  messages are still answered.
- To answer only group messages that start with a keyword, set
  `group_trigger.prefixes` in `config.json`, on the daemon or on a pinned
  account, and leave **Mention Only** off:

  ```json
  {
    "channels": {
      "secmsg": [
        {
          "name": "signal",
          "enabled": true,
          "address": "127.0.0.1:9801",
          "allow_from": ["signal:1a2b3c4d-…"],
          "group_trigger": { "prefixes": ["!alice"] }
        }
      ]
    }
  }
  ```

  The prefix is removed before the agent sees the message.

A pinned account uses the daemon's group setting unless it turns on **Mention
Only** or sets prefixes of its own.

## Which agent answers

Bind each account's channel, for example `signal-alice`, to an agent on the
**Bindings** page. The page lists the channels of every enabled daemon.

To send one group or one person to a different agent, add a binding with a
`peer` in `config.json` (see
[Routing and bindings](./routing.md#bind-one-chat-to-an-agent)):

- a group: kind `group` and the group's ID (the `chat_id` described below,
  without `group:`);
- a person: kind `direct` and their ID, as in **Allow From** without the
  service in front.

To have scheduled output delivered to SecMsg, choose the account's channel as
the agent's [default channel](./routing.md#the-default-channel) and enter, as
the chat ID, the person's ID or `group:` followed by the group's ID. Both
appear on the **Logs** page as `chat_id` on the "Processing message from
signal-alice:…" line.

## Messages, files and voice

- Only text messages and their attachments reach the agent. Edits, deletions
  and reactions are ignored, as are copies of messages sent from your phone.
- Attachments, voice messages included, reach the agent as files. Set up a
  [transcription service](./voice.md) to have voice messages turned into text.
- The agent's replies are text only; a file the agent sends is not delivered.
- Long replies are split into messages of up to 2,000 characters.

## Commands

Commands such as `/status` work here. Start them with `/` or `!`. See
[Chat commands](../reference/chat-commands.md), and
[Messages between agents](../agents/messaging.md) for `/ask` and `/whisper`.

## Test it

1. As someone listed in **Allow From**, send the account a message in the
   messenger app.
2. The agent bound to the account's channel (or the default agent) should
   answer.

## When something goes wrong

- **No answer at all**: check the **Logs** page for "Message from unauthorized
  sender" and add that ID to **Allow From**. In a group, check
  [Groups](#groups).
- **The card says "daemon unreachable"**: the daemon is not running or the
  **Daemon Address** is wrong.
- **An account is missing under Accounts**: it is not linked yet; link it as
  above. If accounts are pinned, only the pinned ones are connected.
- **Another agent answers**: check the account's binding on the **Bindings**
  page.

[Alerts](../admin/alerts.md) for this channel:

- **SecMsg has no linked accounts**: the daemon answered but holds no linked
  account, so no channel was started. Link one as above.
- **SecMsg account discovery failed**: ClawEh could not reach the daemon when
  it started or the configuration was saved, so no channel was started. Start
  the daemon, then save any setting or restart ClawEh; it does not retry on
  its own.
- **SecMsg down: signal-alice**: the account's channel has had no working
  connection to the daemon for ten minutes. ClawEh keeps reconnecting and
  raises **SecMsg up: signal-alice** when it is back.
- **Channel send failed**: a reply was not sent, because the daemon refused
  it or the account was not connected to the daemon at that moment. A reply
  from an account the daemon keeps receive-only is not an error; it is logged
  and not alerted.

The [Check Up](../admin/checkup.md) full report lists each daemon with its
address and who may write; with `*` in **Allow From**, Check Up lists it under
**Channels accepting any sender**.

## Related pages

- [Routing and bindings](./routing.md)
- [Voice messages and transcription](./voice.md)
- [Channels overview](./index.md)
