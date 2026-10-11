# HTTPS and certificates

ClawEh serves the WebUI over plain HTTP on this machine and over HTTPS for other machines, with a self-signed certificate unless you supply your own.

## The two listeners

| Listener | Default port | Default reach | Set on the **Network** page under |
|---|---|---|---|
| HTTP | 18790 | This machine only | **HTTP (WebUI on this host)** → **Scope** |
| HTTPS | 18443 | Every interface | **HTTPS** → **Listen on** |

Both serve the same WebUI and need the same [sign-in](./login.md). Other machines are refused until their network is allowed; see [Network access](./network.md).

**Listen on** for HTTPS has three choices:

- **All interfaces (default)**: reachable from the network.
- **Localhost only**: this machine only, for a reverse proxy on the same host.
- **Off**: no HTTPS; ClawEh is reachable from this machine only.

HTTP **Scope** can be set to **Network**, which serves the WebUI unencrypted on your network. This is not recommended, and [Check Up](./checkup.md) marks it.

Ports, scope and **Listen on** take effect after a restart. The page says "Restart required to apply changes." and offers **Restart now**.

## Open the WebUI over HTTPS

1. Run `claw status` on the server, or look at **Addresses** on the **Network** page, for the address to open.
2. Browse to it, for example `https://<server>:18443/`.
3. The browser warns once about the self-signed certificate. Compare the SHA-256 fingerprint it shows with the one on the **Network** page (**Certificate** → **SHA-256 fingerprint**) or from `claw tls`, and accept it only if they match.

### Reaching it by a name

ClawEh answers only to names it knows. To use a name such as `claw.home.arpa`:

1. On the **Network** page, under **HTTPS**, set **Hostname / external URL** to the address you type, for example `https://claw.home.arpa:18443`.
2. Add any other names or addresses (a DNS alias, a NAT address) to **Extra certificate names**, pressing Enter after each.
3. Under **Certificate**, click **Regenerate certificate** so the new names are in it.

A request for a name ClawEh does not know is refused with "421 Misdirected Request".

## The self-signed certificate

With no certificate files set, ClawEh creates its own the first time HTTPS starts, in `<CLAW_HOME>/tls/`. It covers the machine's host name, its addresses, the external URL's host, the extra names, and `localhost`. It is valid for ten years.

ClawEh renews it by itself within 30 days of expiry, and at start when the names have changed. To renew it now, use **Regenerate certificate** on the **Network** page or run:

```
claw tls --regenerate
```

Your browser will warn again after a new certificate is issued.

## Use your own certificate

1. Put the PEM certificate (with its chain) and the private key where the service user can read them, for example `/etc/letsencrypt/live/claw.example.com/`.
2. On the **Network** page, under **Certificate**, choose **External certificate**.
3. Enter **Certificate file** and **Private key file** as full paths.
4. Click **Save certificate**.

ClawEh loads the pair first and saves the paths only if it works: both files readable, the key matching the certificate, and the certificate currently valid.

The same keys in `config.json`:

```json
"gateway": {
  "tls": {
    "cert_file": "/etc/letsencrypt/live/claw.example.com/fullchain.pem",
    "key_file":  "/etc/letsencrypt/live/claw.example.com/privkey.pem"
  }
}
```

### Renewals

ClawEh checks the files every minute and switches to a renewed pair without a restart, so a certbot or acme.sh renewal only has to write the files. If a new pair does not load, the old one keeps serving and the "TLS certificate reload failed" [alert](./alerts.md) is raised. Alerts also fire 14 days and 3 days before your certificate expires.

### Go back to self-signed

On the **Network** page, under **Certificate**, click **Use self-signed certificate**.

## Inspect the certificate

On the **Network** page, **Certificate** shows the **Source** (Self-signed or From file), **Subject**, **Names**, **Expires** and **SHA-256 fingerprint**.

On the server:

```
claw tls        # the certificate in full
claw status     # the addresses, the certificate's expiry and fingerprint
```

## Other listeners

- The device listener has its own port and can use the same certificate; see [Network access](./network.md#the-device-listener).
- The MCP host, which local CLI tools use, always stays on this machine.
