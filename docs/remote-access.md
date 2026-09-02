# Remote access

ClawEh listens on **two separate HTTP ports**, both bound to localhost by default:

| Port | Default | Serves | Authentication |
|---|---|---|---|
| **WebUI port** (`gateway.port`) | `18790` | WebUI, `/api/*`, the WebUI chat WebSocket | **None** on `/api/*` |
| **Device gateway port** (`channels.device.port`) | `18791` | The OpenClaw Gateway WebSocket for paired devices (Rabbit R1, Claw to Talk) | Shared or per-device token + Ed25519 pairing |

They are **independent listeners** — publishing one does not publish the other.
Decide which you actually need before you expose anything:

- Reaching the **web console** from outside the machine → publish the WebUI port.
- Letting an **external device** connect to the gateway → publish the device
  gateway port. This is the common case, and it does **not** require exposing the
  WebUI.

Three common approaches are below. All of them work because both surfaces are
ordinary HTTP + WebSocket; you are just publishing a port.

> **Security note:** The WebUI and API have **no authentication** — access control
> is the bind address plus `gateway.allowed_cidrs`, which are two independent
> gates. `allowed_cidrs` is empty by default, meaning **loopback only**: binding
> to `0.0.0.0` alone will not serve a network client, and you must add the
> networks you want to reach it from — a subnet such as `192.168.1.0/24`, or `*`
> for any address. Use `*` rather than `0.0.0.0/0` when you mean "everything":
> that is an IPv4 prefix, so it still refuses IPv6 clients. `claw network` sets
> this from a shell on the host without editing the config, and a running gateway
> applies it on its next config reload (about 15 seconds). Whichever method you
> choose, treat an exposed WebUI endpoint as sensitive and restrict access at the
> edge (client certificates, SSO, an allowlist, or a private overlay network)
> until in-app auth is in place. The device gateway authenticates every client, so
> it is the safer of the two to publish.

Replace `<port>` with the port you are publishing — `18790` for the WebUI,
`18791` for the device gateway — throughout.

---

## Cloudflare Tunnel

Best when you have a domain on Cloudflare and want a real HTTPS hostname with no
inbound firewall changes. The tunnel dials *out* from your machine, so it works
behind NAT and dynamic IPs.

```bash
# Install cloudflared (see Cloudflare docs for your platform), then:
cloudflared tunnel login
cloudflared tunnel create claw
cloudflared tunnel route dns claw claw.example.com

# Run the tunnel, pointing it at ClawEh:
cloudflared tunnel --url http://127.0.0.1:<port> run claw
```

Or via a config file (`~/.cloudflared/config.yml`):

```yaml
tunnel: claw
credentials-file: /home/user/.cloudflared/<tunnel-id>.json
ingress:
  - hostname: claw.example.com
    service: http://127.0.0.1:<port>
  - service: http_404
```

Cloudflare proxies WebSockets automatically; no extra configuration is needed.
TLS is terminated at Cloudflare's edge with a managed certificate.

---

## Tailscale

Best for private, device-to-device access with no public exposure at all. Every
machine on your tailnet can reach ClawEh directly.

```bash
# Install Tailscale and bring the node up:
tailscale up
```

Then browse to `http://<machine-name>:<port>` from any device on your tailnet.

To publish it to the public internet over HTTPS with a Tailscale-managed
certificate, use **Funnel**:

```bash
tailscale funnel <port>
```

Tailscale carries WebSocket traffic transparently. Funnel provides HTTPS and a
stable `*.ts.net` hostname; the rest of your tailnet stays private.

---

## NGINX

Best when you already run NGINX, or want full control over TLS and access policy
on your own host. Use this when you have a stable route to the ClawEh machine
(same host, LAN, VPN, or SD-WAN).

```nginx
# Collapse the Connection header per request (http{} scope, once per server):
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
        # ClawEh on the same host. Change to another IP (LAN/VPN/SD-WAN) as needed,
        # e.g. http://10.0.0.5:<port>
        proxy_pass http://127.0.0.1:<port>;

        # WebSocket upgrade — required for the device gateway and WebUI live updates
        proxy_http_version 1.1;
        proxy_set_header Upgrade    $http_upgrade;
        proxy_set_header Connection $connection_upgrade;

        # Preserve client/protocol info
        proxy_set_header Host              $host;
        proxy_set_header X-Real-IP         $remote_addr;
        proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;

        # WebSockets are long-lived; raise timeouts so idle connections survive
        proxy_read_timeout  3600s;
        proxy_send_timeout  3600s;
    }
}
```

Notes:

- A single `location /` covers the WebUI, the API, and the WebUI chat WebSocket —
  the `Upgrade`/`Connection` headers route plain HTTP and WebSocket correctly.
  The **device gateway is a different port** and needs its own `server` block (see
  [Device gateway](#device-gateway) below).
- ClawEh's built-in IP allowlist matches the TCP peer, which behind NGINX is
  NGINX itself. Enforce access control at NGINX, and allow NGINX's source address
  in ClawEh's `--allowed-cidrs` (or set `*` and rely on NGINX).
- If a WebSocket Origin allowlist is configured, include your public origin
  (e.g. `https://claw.example.com`).

---

## Device gateway

The device gateway is a **separate listener** (`channels.device.port`, default
`18791`) that speaks the OpenClaw Gateway WebSocket protocol. Publish this port —
not the WebUI port — when the goal is to let a Rabbit R1 or the Claw to Talk app
reach your agents from outside the LAN.

Two things make it different from the WebUI port:

- **It is authenticated.** Every client presents a shared token (the long QR
  token or the 5-word `word_token` passphrase) and then completes per-device
  Ed25519 pairing approval. Exposing it does not expose your config or tokens.
- **It accepts a WebSocket upgrade on any path.** Devices connect to
  `ws://<host>:<port>/` with no path; a non-WebSocket request gets a 404.

TLS is **not** terminated in ClawEh. Put a reverse proxy in front that terminates
`wss://` and forwards plain `ws://` to the device port:

```nginx
server {
    listen 443 ssl;
    server_name gateway.example.com;

    ssl_certificate     /etc/letsencrypt/live/gateway.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/gateway.example.com/privkey.pem;

    location / {
        proxy_pass http://127.0.0.1:18791;

        # Required — this endpoint is WebSocket-only
        proxy_http_version 1.1;
        proxy_set_header Upgrade    $http_upgrade;
        proxy_set_header Connection $connection_upgrade;

        proxy_set_header Host              $host;
        proxy_set_header X-Real-IP         $remote_addr;
        proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;

        proxy_read_timeout  3600s;
        proxy_send_timeout  3600s;
    }
}
```

The same `$connection_upgrade` map from the NGINX section above applies.

Then tell ClawEh what to advertise to devices, so the QR and the pairing flow
hand out the public endpoint rather than a LAN address:

```json
{
  "channels": {
    "device": {
      "enabled": true,
      "host": "127.0.0.1",
      "port": 18791,
      "external_url": "https://gateway.example.com"
    }
  }
}
```

`external_url` maps `https` → `wss` automatically. Leave `host` on `127.0.0.1`
when a reverse proxy on the same machine fronts it; set `0.0.0.0` only to accept
direct connections from the local network.

Cloudflare Tunnel and Tailscale work here too — point the ingress/funnel at
`127.0.0.1:18791` instead of the WebUI port. Both carry WebSockets transparently.

> `channels.device.allowed_cidrs` matches the TCP peer, which behind a reverse
> proxy is the proxy itself. Leave it empty (the gateway authenticates every
> client anyway) or allow the proxy's source address.
