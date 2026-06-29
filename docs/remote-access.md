# Remote access

ClawEh serves its WebUI, API, and the device gateway on a single HTTP port
(default **18790**), bound to localhost. To reach it from outside the machine —
for example so an external device can connect to the gateway — you need to
expose that port to the public internet.

Three common approaches are below. All of them work because ClawEh's surface is
ordinary HTTP + WebSocket on one port; you are just publishing that port.

> **Security note:** The WebUI and API currently have no authentication. Whichever
> method you choose, treat the exposed endpoint as sensitive and restrict access
> at the edge (client certificates, SSO, an allowlist, or a private overlay
> network) until in-app auth is in place.

Replace `<port>` with your ClawEh port (default `18790`) throughout.

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

- A single `location /` covers the WebUI, API, and the gateway WebSocket — the
  `Upgrade`/`Connection` headers route plain HTTP and WebSocket correctly.
- ClawEh's built-in IP allowlist matches the TCP peer, which behind NGINX is
  NGINX itself. Enforce access control at NGINX, and allow NGINX's source address
  in ClawEh's `--allowed-cidrs` (or set `0.0.0.0/0` and rely on NGINX).
- If a WebSocket Origin allowlist is configured, include your public origin
  (e.g. `https://claw.example.com`).
