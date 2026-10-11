# Other chat channels

Set up the remaining chat channels ClawEh supports: Matrix and SecMsg (Signal and similar services through a separate daemon).

Each of these works like the others: set it up on its page under
**Channels**, fill in **Allow From**, then bind it to an agent on the
[Bindings](./routing.md) page. Settings save as you type.

## Matrix

ClawEh logs in to a Matrix homeserver as an ordinary Matrix account and answers
in rooms and direct chats.

1. Create a Matrix account for the agent on your homeserver, and get an access
   token for it (your Matrix client or homeserver admin can issue one).
2. In the WebUI sidebar, open **Channels → Matrix** and turn on **Enable
   channel**.
3. Fill in:
   - **Homeserver**: the homeserver URL, for example `https://matrix.org`.
   - **User Id**: the account's full ID, for example `@alice:example.org`.
   - **Access Token**: the token from step 1.
   - **Device Id**: optional.
   - **Join On Invite**: on by default; the account joins any room it is
     invited to.
4. In **Allow From**, list who may write, each by full user ID, for example
   `@bob:example.org`. An empty list lets nobody in; `*` lets anyone in.
5. Bind the channel `matrix` to an agent.

Notes:

- In rooms with other people, the account answers only when mentioned (this is
  the default for Matrix). Direct chats are always answered.
- Replies are sent as formatted text. Set `message_format` to `plain` in
  `config.json` for plain text.
- Messages sent while ClawEh was stopped are not answered when it starts.
- If the homeserver rejects the token, ClawEh raises **Channel credentials
  rejected**; with no working connection for ten minutes it raises **Matrix
  down**, then **Matrix up** when it is back.

## SecMsg (Signal)

SecMsg connects ClawEh to a separate secure-messaging daemon, such as `sigd`
for Signal. ClawEh does not include the daemon; install and run it first and
note its address (host and port).

One daemon can host several accounts (phone numbers). Each account becomes its
own channel, named `<daemon name>-<account>`, so each can be bound to a
different agent.

1. In the WebUI sidebar, open **Channels → SecMsg** and click **Add Daemon**.
2. Enter a name, for example `signal`; it starts every channel name of this
   daemon.
3. Fill in **Daemon Address**, for example `127.0.0.1:9600`.
4. In **Allow From**, list who may write to any account on this daemon. An
   empty list lets nobody in.
5. Turn on **Mention Only** if the accounts should answer in groups only when
   mentioned.
6. Click **Save**. Under **Accounts**, ClawEh lists the accounts the daemon
   already has, each with the channel name it will use.
7. To link an account to the messaging app on your phone, click **Link
   device** under that account and scan the QR code from the app's
   linked-devices screen. The page shows **Linked** when it is done.
8. Bind each account's channel to an agent.

To give one account its own name, allow list or group setting, click **Show
advanced: pin specific accounts**, then **Pin account**. A pinned account has
its own **Device Linking** section. If the daemon has no account yet, pin one
and link it there.

[Alerts](../admin/alerts.md): **SecMsg has no linked accounts** (link one as above), **SecMsg
account discovery failed** (the daemon could not be reached; it is asked again
at the next configuration reload), and **SecMsg down** after ten minutes
without a connection.

## Related pages

- [Channels overview](./index.md)
- [Routing and bindings](./routing.md)
