# Matrix

Connect a Matrix account so people can talk to your agents in Matrix rooms and direct chats.

ClawEh logs in to your homeserver as an ordinary Matrix user and fetches
messages itself, so you do not need a public address. You need:

- a Matrix account for the agent, for example `@alice:example.org`;
- an **access token** for that account.

End-to-end encrypted rooms are not supported. See
[Encryption](#encryption) before you create rooms.

## Create the account and get a token

1. Create a Matrix account for the agent on your homeserver, the same way you
   would for a person. Use a separate account, not your own: the agent answers
   as this account.
2. Get an access token for it. The simplest way is to log in once with the
   homeserver's login API, which creates a session just for ClawEh:

   ```sh
   curl -s -X POST https://example.org/_matrix/client/v3/login \
     -H 'Content-Type: application/json' \
     -d '{"type":"m.login.password","identifier":{"type":"m.id.user","user":"alice"},"password":"…","initial_device_display_name":"ClawEh"}'
   ```

   The answer contains `access_token`. Keep it secret: anyone with the token
   can read and write as the account.

You can also copy the token of a session in a Matrix app, but logging that
session out later revokes the token, and ClawEh stops receiving messages.

## Connect ClawEh

1. In the WebUI sidebar, open **Channels → Matrix**.
2. Turn on **Enable channel**.
3. Fill in:
   - **Homeserver**: the homeserver URL, for example `https://example.org`.
   - **User Id**: the account's full user ID, for example
     `@alice:example.org`.
   - **Access Token**: the token from above. Once saved it is shown masked;
     leave the field blank to keep it.
4. In **Allow From**, enter who may write, separated by commas (see
   [Who may write](#who-may-write)).
5. Leave **Join On Invite** on if the account should join every room it is
   invited to.
6. Open [Bindings](./routing.md) and bind the channel `matrix` to the agent
   that should answer. Without a binding, the
   [default agent](../agents/creating.md#the-default-agent-and-default-models)
   answers.

Settings save as you type. **Homeserver**, **User Id** and **Access Token**
are required while the channel is enabled. The channel name, which you use in
bindings, is always `matrix`; ClawEh connects one Matrix account.

### Other settings on the page

- **Group Mention Only**: in rooms with other people, answer only when the
  account is mentioned. On by default. See [Rooms](#rooms).
- **Group Trigger Prefixes**: answer room messages that start with one of
  these texts. See [Rooms](#rooms).
- **Placeholder Message**: posts a short notice (by default "Thinking... 💭")
  as soon as a message arrives, and edits it into the reply. On by default.
  The text box under it changes the notice.

While the agent works, the account also shows as typing in the room.

Replies are sent as formatted text (the agent's Markdown is turned into
Matrix formatting). To send plain text, set `"message_format": "plain"` under
`channels.matrix` in `config.json`.

## Test it

1. In your Matrix app, start a chat with the agent's account, for example
   `@alice:example.org`, with encryption off.
2. With **Join On Invite** on, the account joins at once. Otherwise, join the
   room as the agent account yourself.
3. Send a message. The agent bound to `matrix` (or the default agent) should
   answer.

## Who may write

**Allow From** lists the Matrix users who may use the agent. An empty list
lets nobody in; `*` lets in anyone who can reach the account (not
recommended).

Enter each person by their full user ID, for example `@bob:example.org`. The
short form `@bob` does not work.

**Allow From** is checked against the person who sends the message, also in
rooms: in a room, only listed people get an answer. Messages from anyone else
are ignored without a reply.

**Join On Invite** does not check **Allow From**: the account joins a room
whoever invites it, but still answers only listed people there.

## Direct chats and rooms

ClawEh treats a room with two members (the agent's account and one person) as
a direct chat, and a room with more members as a group room. A direct chat is
always answered.

### Rooms

In a room with more than two members, what the account answers depends on two
settings:

- **Group Mention Only** on (the default): only messages that mention the
  account. A mention is a mention "pill" from your Matrix app, the full user
  ID (`@alice:example.org`) or `@alice` in the text. The mention is removed
  before the agent sees the message.
- **Group Mention Only** off and **Group Trigger Prefixes** set, for example
  `!alice`: messages that mention the account or start with a prefix. The
  prefix is removed before the agent sees the message.
- Both off: every message from a listed sender.

With **Group Mention Only** on, the prefixes are not used.

## Which agent answers

Bind the channel `matrix` to an agent on the **Bindings** page; that agent
answers every Matrix message.

To send one room or one person to a different agent, add a binding with a
`peer` in `config.json` (see
[Routing and bindings](./routing.md#bind-one-chat-to-an-agent)):

- a room: kind `group` and the room ID, for example
  `!AbCdEfGh:example.org`;
- a person's direct chats: kind `direct` and their user ID, for example
  `@bob:example.org`.

A room ID is shown in your Matrix app's room settings (in Element: **Settings
→ Advanced → Internal room ID**), and on the **Logs** page as `chat_id` on the
"Processing message from matrix:…" line.

To have scheduled output delivered to Matrix, choose `matrix` as the agent's
[default channel](./routing.md#the-default-channel) and enter a room ID as the
chat ID, also for a direct chat.

## Encryption

ClawEh does not support Matrix end-to-end encryption. In an encrypted room it
cannot read the messages, so it never answers there, and nothing is logged.

- Create the chat or room with encryption turned off. Many Matrix apps turn
  encryption on for new direct chats by default, and once a room is encrypted
  it cannot be turned off; create a new room instead.
- The connection to the homeserver itself is protected by HTTPS when the
  **Homeserver** URL starts with `https://`.

## Files and voice messages

People can send images, audio, video and other files; the agent receives them
as attachments, with the file name and any caption. Voice messages are audio
files: set up a [transcription service](./voice.md) to have them turned into
text.

The agent can send files back; they are uploaded to the homeserver and posted
in the room.

ClawEh ignores message edits and other kinds of events (reactions, locations,
polls). Messages sent while ClawEh was stopped are not answered when it
starts. Saving any settings in the WebUI restarts the channel, and messages
sent during that moment are not answered either.

## Commands

Commands such as `/status` work in Matrix. Start them with `/` or `!`; some
Matrix apps treat `/` themselves, so use `!status` there. See
[Chat commands](../reference/chat-commands.md), and
[Messages between agents](../agents/messaging.md) for `/ask` and `/whisper`.

## When something goes wrong

- **No answer at all**: check that the sender's full user ID is in **Allow
  From**, that the room is not encrypted, and, in a room with more than two
  members, that the message mentions the account (or turn off **Group Mention
  Only**).
- **The account does not join**: turn on **Join On Invite**, or join the room
  as the account yourself. A failed join is logged as a warning.
- **Another agent answers**: check the `matrix` binding on the **Bindings**
  page.
- **Channel credentials rejected** ([alert](../admin/alerts.md)): the
  homeserver refused the access token, for example because its session was
  logged out. Get a new token and paste it into **Access Token**. ClawEh keeps
  trying every minute in case the token is restored.
- **Matrix down: matrix** (alert): no working connection to the homeserver for
  ten minutes. ClawEh keeps reconnecting and raises **Matrix up: matrix** when
  it is back.
- **Channel send failed** (alert, for `matrix`): a reply could not be posted
  after several tries, for example because the account has left the room.

The [Check Up](../admin/checkup.md) full report lists the channel with its
homeserver and account, who may write and when it answers in rooms; with `*`
in **Allow From**, Check Up lists it under **Channels accepting any sender**.

## Related pages

- [Routing and bindings](./routing.md)
- [Voice messages and transcription](./voice.md)
- [Channels overview](./index.md)
