# Remote access

Reach ClawEh from other machines, from outside your home or office, or through a reverse proxy or tunnel.

This page shows the common setups and what to set for each. The settings
themselves are described in [Network access](./network.md) and
[HTTPS and certificates](./https.md).

## Decide what needs to be reached

ClawEh has two separate front doors. Opening one does not open the other.

| What | Listener | Default port | Who uses it |
|---|---|---|---|
| The WebUI, its API and the [Message API](../channels/message-api.md) | HTTPS (and HTTP on this machine) | 18443 (HTTP 18790) | You, in a browser; apps that post messages |
| The device listener | Its own | 18791 | Rabbit R1, voice apps (see [Devices](../channels/devices.md)) |

Chat channels such as Telegram, Slack and Discord need neither: ClawEh connects
out to them. LINE is the exception; it needs the WebUI address to be reachable
from the internet (see [Other chat channels](../channels/other.md#line)).

Whatever you choose, every WebUI request needs the [admin sign-in](./login.md),
and every device must be paired.

## Choose a setup

| Setup | Good for | You set |
|---|---|---|
| [HTTPS on your network](#https-on-your-network) | Other machines on the same LAN or VPN | **Allowed network CIDRs** |
| [Reverse proxy](#reverse-proxy-nginx-or-caddy) | A public name with a real certificate, on a server you run | **Trusted proxies**, **Hostname / external URL** |
| [Cloudflare Tunnel](#cloudflare-tunnel) | A public name with no open ports on your router | As for a reverse proxy |
| [Tailscale](#tailscale) | Your own devices only, from anywhere | **Allowed network CIDRs**, a name for the certificate |

## HTTPS on your network

This needs no other software. HTTPS listens on every interface by default.

1. On the **Network** page, under **Allowed networks**, add your network to
   **Allowed network CIDRs**, for example `192.168.1.0/24`.
2. Open the address shown under **Addresses** (**On the network (HTTPS)**),
   for example `https://192.168.1.20:18443/`.
3. Accept the self-signed certificate after checking its fingerprint (see
   [HTTPS and certificates](./https.md#open-the-webui-over-https)).

To use a name instead of the address, see
[Reaching it by a name](./https.md#reaching-it-by-a-name).

A VPN that puts you on this network (WireGuard, OpenVPN, your router's VPN)
works the same way: allow the VPN's address range too.

## Reverse proxy (NGINX or Caddy)

The proxy holds the public name and certificate and passes requests to
ClawEh's HTTP listener on the same machine.

### What to set in ClawEh

On the **Network** page:

1. Leave **HTTP (WebUI on this host)** → **Scope** on localhost. The proxy
   connects to `http://127.0.0.1:18790`.
2. Under **HTTPS**, set **Hostname / external URL** to the public address, for
   example `https://claw.example.com`. ClawEh answers only to names it knows;
   without this, requests through the proxy get "421 Misdirected Request".
3. Optionally set HTTPS **Listen on** to **Localhost only** or **Off**, so the
   proxy is the only way in from the network.
4. Under **Allowed networks**, add `127.0.0.1` to **Trusted proxies**.
5. Set **Allowed network CIDRs** to the clients the proxy may pass on: `*` for
   anyone who can reach the proxy, or a narrower list.

Steps 4 and 5 go together. A proxy on the same machine connects from
localhost, which is always allowed. Without **Trusted proxies**, every request
looks like it comes from localhost: the allowed networks never apply, and
failed sign-ins never lock out an address, because localhost never is. With it, ClawEh uses the client
address the proxy reports, from the `X-Real-IP` header or else the first
`X-Forwarded-For` entry. See
[Behind a reverse proxy](./network.md#behind-a-reverse-proxy).
If step 4 is missing, the **Network** page and [Check Up](./checkup.md) say
"Requests arrive through a proxy that is not trusted".

Ports, scope and **Listen on** need a restart; the page offers **Restart now**.

### NGINX

```nginx
# Once, in the http {} block:
map $http_upgrade $connection_upgrade {
    default upgrade;
    ''      close;
}

server {
    listen 443 ssl;
    server_name claw.example.com;

    ssl_certificate     /etc/letsencrypt/live/claw.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/claw.example.com/privkey.pem;

    location / {
        proxy_pass http://127.0.0.1:18790;

        # The WebUI chat uses a WebSocket.
        proxy_http_version 1.1;
        proxy_set_header Upgrade    $http_upgrade;
        proxy_set_header Connection $connection_upgrade;

        proxy_set_header Host              $host;
        proxy_set_header X-Real-IP         $remote_addr;
        proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;

        # Keep idle WebSockets open.
        proxy_read_timeout  3600s;
        proxy_send_timeout  3600s;
    }
}
```

### Caddy

Caddy gets the certificate itself and passes WebSockets without extra
settings.

```
claw.example.com {
    reverse_proxy 127.0.0.1:18790 {
        header_up X-Real-IP {remote_host}
    }
}
```

## Cloudflare Tunnel

The tunnel connects out from your machine to Cloudflare, so nothing is opened
on your router. Cloudflare holds the certificate for your name.

```bash
cloudflared tunnel login
cloudflared tunnel create claw
cloudflared tunnel route dns claw claw.example.com
cloudflared tunnel --url http://127.0.0.1:18790 run claw
```

Then set ClawEh as for a [reverse proxy](#what-to-set-in-claweh): **Hostname /
external URL** `https://claw.example.com`, **Trusted proxies** `127.0.0.1`,
and **Allowed network CIDRs** `*` (or the networks you expect). Cloudflare
passes the visitor's address in `X-Forwarded-For`.

Anyone on the internet can now reach the sign-in page. To limit who gets that
far, put Cloudflare Access in front of the name.

## Tailscale

Tailscale connects your own devices in a private network (a tailnet). Nothing
is public.

### Directly over the tailnet

1. Install Tailscale on the ClawEh machine and on your devices, and run
   `tailscale up`.
2. On the **Network** page, add the tailnet range `100.64.0.0/10` to
   **Allowed network CIDRs**.
3. To open ClawEh by its tailnet name (for example `claw.tailnet-name.ts.net`),
   add that name to **Extra certificate names** under **HTTPS** and click
   **Regenerate certificate**. Opening it by its `100.x.y.z` address works
   without this.
4. Browse to `https://<name or address>:18443/` and accept the self-signed
   certificate.

### With a Tailscale certificate

`tailscale serve` gives ClawEh an HTTPS address with a real certificate on your
tailnet and passes requests to ClawEh on this machine:

```bash
tailscale serve --bg http://127.0.0.1:18790
```

Set ClawEh as for a [reverse proxy](#what-to-set-in-claweh), with **Hostname /
external URL** set to the `https://….ts.net` address Tailscale prints.

`tailscale funnel` does the same but publishes the address to the whole
internet. Use it only if you mean to.

## Devices from outside

The device listener is separate. For devices on your network, set
**Device listener** → **Scope** to **Network** on the **Network** page (see
[The device listener](./network.md#the-device-listener)).

For devices outside your network, put a proxy or tunnel in front of port 18791,
as above, on its own name:

1. Point the proxy, tunnel or `tailscale serve` at `http://127.0.0.1:18791`.
   It must pass WebSockets (the NGINX block above does; use a separate
   `server` block with `proxy_pass http://127.0.0.1:18791;`).
2. Under **Device listener**, set **External address** to the full public
   address, for example `https://devices.example.com`. Devices are then given
   `wss://devices.example.com` in the QR code and pairing details. Leave
   **Protocol** at **ws**: the proxy does the encryption.
3. Pair the device again so it gets the new address.

With **Trusted proxies** set, the device listener also uses the address the
proxy reports, for its lockouts and its allowed networks. Without it, every
device appears to come from the proxy.

Leave the device listener's **Scope** on localhost when only a proxy on this
machine connects to it. On **Network**, ClawEh refuses to start with
**Auto-approve pairings** on or without a device token.

## Check the result

- **Addresses** on the **Network** page, and `claw status` on the server, show
  where ClawEh can be opened.
- [Check Up](./checkup.md) shows one row per listener, whether it is
  encrypted, and which networks are allowed.

## Related pages

- [Network access](./network.md)
- [HTTPS and certificates](./https.md)
- [Signing in](./login.md)
- [Devices](../channels/devices.md)
