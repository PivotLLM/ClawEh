# Devices

Pair a Rabbit R1 or a voice app with ClawEh so it can talk to your agents.

Devices connect to the **device listener**, a separate port of ClawEh (18791
by default) used only by devices. Every device needs two things: the shared
secret (from a QR code or a typed passphrase), and your approval of its
pairing on the **Devices** page.

## Before you start: let devices reach the listener

The device listener starts on localhost only, so other machines cannot reach
it yet.

1. Open **Services → Network** and find **Device listener** (see
   [Network access](../admin/network.md)).
2. Set **Scope** to **Network**.
3. Optional settings in the same section:
   - **Port**: 18791 by default.
   - **External address**: the address devices use from outside, for example
     through a reverse proxy. Leave blank on a home network; the QR code then
     carries this machine's network address.
   - **Allowed network CIDRs**: limit which networks may connect. Empty allows
     any; localhost is always allowed.
   - **Protocol**: **ws (unencrypted)** or **wss (HTTPS)**; see
     [Encryption](#encryption).
   - **Auto-approve pairings**: skips your approval. It is allowed only while
     the listener is on localhost.

A listener on the network always needs a secret and your approval: ClawEh
refuses a configuration with **Auto-approve pairings** on, or with no secret,
while **Scope** is **Network**. **Generate pairing QR** (below) creates the
secret for you.

## Pair a Rabbit R1 (QR code)

1. Open **Services → Devices**.
2. Under **Pair a device**, click **Generate pairing QR**. The first time, this
   also turns the device listener on and creates its secrets. The card shows
   the address devices will connect to.
3. Scan the QR code with the R1.
4. The R1 now appears under **Pending approvals**, with the address it
   connected from, for example "Rabbit R1 · from 192.168.1.40". Check that the
   address is the device you expect.
5. Click **Approve**. The R1 connects and can talk to its agent.

A pairing request waits ten minutes; after that, connect again from the device.
At most 20 requests wait at once. Click **Reject** for any request you do not
recognise.

## Pair a voice app (typed passphrase)

Apps that cannot scan the QR code, such as the "Claw to Talk" Android app, use
the **Profile Token**: five words separated by dashes.

1. Open **Services → Devices**. If no profile token is shown, click **Generate
   pairing QR** once.
2. Under **Profile Token (for apps that can't scan the QR)**, click **Copy**,
   and enter the words into the app's profile-token field together with the
   connect address shown on the page.
3. Approve the app under **Pending approvals**.

The QR secret and the profile token are equal: either one gets a device as far
as the approval. **Regenerate** next to the profile token replaces it. A paired device
that connects with its own device token (issued after approval) keeps working;
an app that always connects with the profile token needs the new one.

## Which agent a device talks to

Every device joins its agent's one conversation, the same conversation as
that agent's Telegram, Slack and WebUI chats. Tell Alice something on the R1
and she knows it in Slack. To keep a device separate, point it at a different
agent.

- **Rabbit R1 and other devices without an agent picker.** Under **Paired
  devices**, choose the agent in the **Assistant** list next to the device.
  **Default assistant** means the default agent.
- **Apps with their own agent picker**, such as the Android app, show
  "assistant chosen in app"; the person picks the agent in the app.

### How an agent replies on a device

While the device channel is enabled, each agent's directory has a `DEVICE.md`,
read only for messages from devices and shared by all of them. Edit it to make
that agent brief on a small screen without changing how it replies elsewhere,
empty it to add nothing, or delete it and the default returns on the next
start. An older `channel-device.md` is renamed to `DEVICE.md` on the next
start. See [Creating and editing agents](../agents/creating.md).

### Switching agents from the device

On a device without a picker, the person can send:

| Command | Effect |
|---|---|
| `/agent` | Lists the assistants and marks the current one. |
| `/agent Bob` | Switches this device to Bob (by name or ID). The reply is "Switched to Bob. New messages will go to this assistant." |
| `/agent default` | Goes back to the default agent. |
| `/help` | Lists these commands. |

The choice is the same setting as the **Assistant** list on the Devices page,
and it survives restarts.

### When the assigned agent is gone

If a device's agent is deleted, disabled or set aside, the device talks to the
default agent instead, and the Devices page shows the reason under the device,
for example "Ignored: Bob no longer exists; using the default agent." Choose
another agent in the **Assistant** list, or have the person send `/agent`.

Agents that represent a person ([human agents](../agents/human-agents.md)) are never offered to devices.

## Encryption

By default devices connect over plain WebSocket (`ws://`). To encrypt:

1. Open **Services → Network → Device listener**.
2. Set **Protocol** to **wss (HTTPS)**. This uses the same certificate as the
   WebUI's HTTPS, so HTTPS must not be **Off** (see
   [HTTPS and certificates](../admin/https.md)).
3. Pair or re-pair devices: the QR code and the connect address now use
   `wss://`. The device must accept the certificate; with the self-signed
   one, check that it does.

If a reverse proxy in front of ClawEh already provides `wss://`, you can leave
**Protocol** at **ws** and set **External address** to the proxy's address.

## Remove a device

1. Open **Services → Devices**.
2. Under **Paired devices**, click **Remove** next to the device.

The device is disconnected at once and its tokens stop working. To use it
again, pair it again.

## When a device is offline

A message for a paired device that is not connected (for example a scheduled
reminder) is not delivered. ClawEh records that the device **is offline**,
logs a warning and does not retry or raise an alert; an agent that sent the
message is told "Not sent: device:… is offline." See
[When a message cannot be delivered](./index.md#when-a-message-cannot-be-delivered).

Five failed connection attempts from one address within ten minutes lock that
address out for a while and raise **Device authentication locked out** (an
[alert](../admin/alerts.md)). A
device that keeps using a revoked token causes this; pair it again once the
lockout ends.

## Related pages

- [Channels overview](./index.md)
- [Routing and bindings](./routing.md)
